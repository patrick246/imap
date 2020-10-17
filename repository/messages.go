package repository

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend/backendutil"
	"github.com/emersion/go-message/textproto"
	"github.com/patrick246/imap/connection/mongodbConnection"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/gridfs"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/x/mongo/driver/uuid"
	"io"
	"net/mail"
	"regexp"
	"strings"
	"time"
)

const messageCollection = "messageMetadata"

type Headers map[string]string
type Message struct {
	ID        string `bson:"_id"`
	MailboxId string `bson:"mailboxId"`
	Uid       uint32

	Header        Headers
	Flags         []string
	Recent        bool
	InternalDate  time.Time           `bson:"internalDate"`
	SentDate      time.Time           `bson:"sentDate"`
	BodyStructure *imap.BodyStructure `bson:"bodyStructure"`
	Size          uint32
}

type MessageRepository struct {
	conn          *mongodbConnection.Connection
	messageBucket *gridfs.Bucket
}

type ChangeStreamEvent struct {
	Id            bson.M `bson:"_id"`
	OperationType OperationType
	FullDocument  *Message
	Ns            MessageUpdateNamespace
	To            *MessageUpdateNamespace
	DocumentKey   struct {
		Id string `bson:"_id"`
	}
	UpdateDescription *struct {
		UpdatedFields bson.M
		RemovedFields []string
	}
	ClusterTime time.Time
	txnNumber   *uint64
	lsid        *struct {
		Id  uuid.UUID
		Uid primitive.Binary
	}
}

type OperationType string

const (
	OpInsert OperationType = "insert"
	OpDelete OperationType = "delete"
)

type MessageUpdateNamespace struct {
	Db   string
	Coll string
}

func NewMessageRepository(conn *mongodbConnection.Connection) (*MessageRepository, error) {
	mailboxUidIndex := mongo.IndexModel{
		Keys: bson.D{
			{"mailboxId", 1},
			{"uid", 1},
		},
		Options: options.Index().SetUnique(true),
	}

	bucket, err := gridfs.NewBucket(conn.Database(), options.GridFSBucket().SetName("messages"))
	if err != nil {
		return nil, err
	}

	_, err = conn.Collection(messageCollection).Indexes().CreateOne(context.Background(), mailboxUidIndex)
	if err != nil {
		return nil, err
	}

	return &MessageRepository{
		conn:          conn,
		messageBucket: bucket,
	}, nil
}

func (repo *MessageRepository) Insert(message *Message) error {
	if message.MailboxId == "" {
		return errors.New("mailbox id can not be empty")
	}

	if message.Uid == 0 {
		return errors.New("message uid can not be zero")
	}

	message.ID = fmt.Sprintf("%s-%d", message.MailboxId, message.Uid)
	result, err := repo.conn.Collection(messageCollection).InsertOne(context.Background(), message)
	if err != nil {
		return err
	}

	log.Debugw("inserted message", "mailbox", message.MailboxId, "uid", message.Uid, "documentId", result.InsertedID)
	return nil
}

/*
	Creates a io.Writer that points to the gridfs bucket for messages
*/
func (repo *MessageRepository) CreateBodyWriter(mailboxId string, uid uint32) (io.WriteCloser, error) {
	bodyId := fmt.Sprintf("%s-%d", mailboxId, uid)
	ws, err := repo.messageBucket.OpenUploadStream(bodyId, options.GridFSUpload().SetMetadata(bson.D{
		{"mailboxId", mailboxId},
		{"uid", uid},
	}))
	return ws, err
}

/*
	Parses a imap literal into a partial message document
	Missing fields:
	- ID (auto assigned by the database)
	- Mailbox ID
	- Uid
*/
func MessageFromBody(flags []string, date time.Time, body io.Reader, bodySize uint32) (*Message, error) {
	bodyBuffer := bufio.NewReader(body)
	header, err := textproto.ReadHeader(bodyBuffer)
	if err != nil {
		return nil, err
	}

	headersMap := make(Headers)
	headerFields := header.Fields()
	for headerFields.Next() {
		headersMap[strings.ReplaceAll(headerFields.Key(), ".", "_")] = headerFields.Value()
	}

	const rfc2822 = "Mon Jan 02 15:04:05 -0700 2006"
	sentDateString := header.Get("Date")
	sentDate, err := time.Parse(rfc2822, sentDateString)
	if err != nil {
		sentDate = time.Time{}
	}

	bodyStructure, err := backendutil.FetchBodyStructure(header, bodyBuffer, true)
	if err != nil {
		return nil, err
	}

	// make sure the document contains a non-nil slice, as mongodb marshals them differently into bson
	docFlags := []string{}
	for _, f := range flags {
		if f == imap.RecentFlag {
			// Don't store the recent flag in the flag set, the server decides what is recent via recent field and a
			// acquisation procedure
			continue
		}
		docFlags = append(docFlags, f)
	}

	return &Message{
		ID:            "",
		MailboxId:     "",
		Uid:           0,
		Header:        headersMap,
		Flags:         docFlags,
		Recent:        true,
		InternalDate:  date,
		SentDate:      sentDate,
		BodyStructure: bodyStructure,
		Size:          bodySize,
	}, nil
}

/*
	The message document "caches" message headers that are not only needed for the envelope, but sometimes
	requested by mail clients on their own. See https://github.com/foxcpp/go-imap-sql/blob/9d2bcb71fca8a3b467b5ae7be92b91656fa0c1e3/sql_fetch.go#L16
	for some popular headers. This function converts some of these headers into an envelope
*/
func EnvelopeFromHeaders(headers Headers) *imap.Envelope {
	envelope := new(imap.Envelope)

	envelope.Date, _ = mail.ParseDate(headers["Date"])
	envelope.Subject = headers["Subject"]
	envelope.From, _ = HeaderAddressList(headers["From"])
	envelope.Sender, _ = HeaderAddressList(headers["Sender"])
	if len(envelope.Sender) == 0 {
		envelope.Sender = envelope.From
	}
	envelope.ReplyTo, _ = HeaderAddressList(headers["Reply-To"])
	if len(envelope.ReplyTo) == 0 {
		envelope.ReplyTo = envelope.From
	}

	envelope.To, _ = HeaderAddressList(headers["To"])
	envelope.Cc, _ = HeaderAddressList(headers["Cc"])
	envelope.Bcc, _ = HeaderAddressList(headers["Bcc"])
	envelope.InReplyTo = headers["In-Reply-To"]
	envelope.MessageId = headers["Message-Id"]

	return envelope
}

func HeaderAddressList(value string) ([]*imap.Address, error) {
	if value == "" {
		return nil, nil
	}
	addrs, err := mail.ParseAddressList(value)
	if err != nil {
		return []*imap.Address{}, err
	}

	list := make([]*imap.Address, len(addrs))
	for i, a := range addrs {
		parts := strings.SplitN(a.Address, "@", 2)
		mailbox := parts[0]
		var hostname string
		if len(parts) == 2 {
			hostname = parts[1]
		}

		list[i] = &imap.Address{
			PersonalName: a.Name,
			MailboxName:  mailbox,
			HostName:     hostname,
		}
	}

	return list, err
}

/*
	Opens a change stream for a mailbox
*/
func (repo *MessageRepository) WatchMailbox(ctx context.Context, mailboxId string) (<-chan ChangeStreamEvent, error) {
	mailboxFilter := mongo.Pipeline{
		bson.D{{
			"$match", bson.D{{
				"documentKey._id", bson.D{{
					"$gt", mailboxId,
				}, {
					"$lt", mailboxId + "\xFF",
				}},
			}, {
				"operationType", bson.D{{
					"$in", bson.A{OpInsert, OpDelete},
				}},
			}},
		}},
	}

	cs, err := repo.conn.Collection(messageCollection).Watch(ctx, mailboxFilter)
	if err != nil {
		return nil, err
	}

	eventChannel := make(chan ChangeStreamEvent)

	go func() {
		log.Infow("starting subscription", "id", mailboxId)
		defer cs.Close(context.Background())
		for {
			if cs.Next(ctx) {
				log.Debugw("got database event", "id", mailboxId)
				var event ChangeStreamEvent
				err := cs.Decode(&event)
				if err != nil {
					log.Errorw("mailbox subscription decode error", "error", err)
					continue
				}
				log.Debugw("database event content", "id", mailboxId, "event", event)
				eventChannel <- event
				continue
			}

			if err := cs.Err(); err != nil {
				log.Errorw("change stream error", "error", err)
			}
			if cs.ID() == 0 {
				log.Infow("shutting down change mailbox subscription")
				break
			}
		}
		log.Infow("end of mailbox subscription", "id", mailboxId)
	}()

	return eventChannel, nil
}

func (repo *MessageRepository) FetchUids(mailboxId string) ([]int, error) {
	query := bson.D{{
		"mailboxId", mailboxId,
	}}

	projection := bson.D{{
		"uid", 1,
	}}

	cursor, err := repo.conn.Collection(messageCollection).Find(context.Background(), query, options.Find().SetProjection(projection))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.Background())

	var uids []int
	for cursor.Next(context.Background()) {
		var messageData bson.M
		err := cursor.Decode(&messageData)
		if err != nil {
			return nil, err
		}
		uid, ok := messageData["uid"]
		if !ok {
			return nil, errors.New("message without uid")
		}

		uidInt, ok := uid.(int64)
		if !ok {
			uidInt32, ok := uid.(int32)
			if !ok {
				return nil, errors.New("non-integer uid")
			}
			uidInt = int64(uidInt32)
		}

		uids = append(uids, int(uidInt))
	}
	return uids, nil
}

func (repo *MessageRepository) FetchMessage(mailboxId string, uid uint32, seqNum uint32, items []imap.FetchItem) (*imap.Message, error) {
	query := bson.D{{
		"mailboxId", mailboxId,
	}, {
		"uid", uid,
	}}

	result := repo.conn.Collection(messageCollection).FindOne(context.Background(), query)

	var messageDoc Message
	err := result.Decode(&messageDoc)
	if err != nil {
		return nil, err
	}

	message := imap.NewMessage(seqNum, items)
	message.Uid = messageDoc.Uid

	for _, item := range items {
		switch item {
		case imap.FetchEnvelope:
			message.Envelope = EnvelopeFromHeaders(messageDoc.Header)
		case imap.FetchBody, imap.FetchBodyStructure:
			bodyStructure := messageDoc.BodyStructure
			if item == imap.FetchBody {
				bodyStructure.Extended = false
			}
			message.BodyStructure = bodyStructure
		case imap.FetchFlags:
			message.Flags = messageDoc.Flags
		case imap.FetchInternalDate:
			message.InternalDate = messageDoc.InternalDate
		case imap.FetchRFC822Size:
			message.Size = messageDoc.Size
		case imap.FetchUid:
			// we're always setting the message uid for further processing
		default:
			section, err := imap.ParseBodySectionName(item)
			if err != nil {
				return nil, err
			}

			if !section.Peek {
				newFlagsMap, err := repo.UpdateFlags(true, &imap.SeqSet{Set: []imap.Seq{{
					Start: message.Uid,
					Stop:  message.Uid,
				}}}, imap.AddFlags, []string{imap.SeenFlag}, []uint32{message.Uid}, mailboxId)
				if err != nil {
					return nil, err
				}
				flags, ok := newFlagsMap[message.Uid]
				if ok {
					message.Flags = flags
					message.Items[imap.FetchFlags] = struct{}{}
				}
			}

			sectionValue, err := repo.getNamedBodySection(section, &messageDoc)
			if err != nil && err.Error() != "backendutil: no such message body part" {
				return nil, err
			} else if err != nil {
				sectionValue = bytes.NewReader([]byte{})
			}
			message.Body[section] = sectionValue
		}
	}

	return message, nil
}

func (repo *MessageRepository) getNamedBodySection(section *imap.BodySectionName, messageDoc *Message) (imap.Literal, error) {
	ds, err := repo.messageBucket.OpenDownloadStreamByName(fmt.Sprintf("%s-%d", messageDoc.MailboxId, messageDoc.Uid))
	if err != nil {
		return nil, err
	}

	body := bufio.NewReader(ds)
	header, err := textproto.ReadHeader(body)
	if err != nil {
		return nil, err
	}

	literal, err := backendutil.FetchBodySection(header, body, section)
	return literal, err
}

func (repo *MessageRepository) FindMessage(criteria *imap.SearchCriteria, uidList []uint32, mailboxId string) ([]uint32, error) {
	query, err := mapSearchCriteriaToQuery(criteria, uidList, mailboxId, 0, false)
	if err != nil {
		return nil, err
	}

	projection := bson.D{{
		"uid", 1,
	}}

	cursor, err := repo.conn.Collection(messageCollection).Find(context.Background(), query, options.Find().SetProjection(projection))
	if err != nil {
		return nil, err
	}

	var resultUids []uint32
	for cursor.Next(context.Background()) {
		var message Message
		err = cursor.Decode(&message)
		if err != nil {
			return nil, err
		}

		resultUids = append(resultUids, message.Uid)
	}

	return resultUids, nil
}

func (repo *MessageRepository) UpdateFlags(uid bool, set *imap.SeqSet, op imap.FlagsOp, flags []string, uids []uint32, mailboxId string) (map[uint32][]string, error) {
	if !uid {
		if len(uids) == 0 {
			return nil, errors.New("sequence set refers to non-existing messages")
		}
		var err error
		set, err = mapSeqNumsToUids(set, uids)
		if err != nil {
			return nil, err
		}
	}

	query := bson.D{{
		"$and", bson.A{
			bson.D{{
				"mailboxId", mailboxId,
			}},
			mapSeqSetToQuery(set, uids[len(uids)-1], false),
		},
	}}

	var update bson.D
	switch op {
	case imap.SetFlags:
		update = bson.D{{
			"$set", bson.D{{
				"flags", flags,
			}},
		}}
	case imap.AddFlags:
		update = bson.D{{
			"$addToSet", bson.D{{
				"flags", bson.D{{
					"$each", flags,
				}},
			}},
		}}
	case imap.RemoveFlags:
		update = bson.D{{
			"$pull", bson.D{{
				"flags", bson.D{{
					"$in", flags,
				}},
			}},
		}}
	default:
		return nil, errors.New(fmt.Sprintf("unknown flag operation: %s", op))
	}

	result, err := repo.conn.Collection(messageCollection).UpdateMany(context.Background(), query, update)
	if err != nil {
		return nil, err
	}
	log.Debugw("flag update", "modified", result.ModifiedCount, "matched", result.MatchedCount)

	cursor, err := repo.conn.Collection(messageCollection).Find(context.Background(), query)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.Background())

	newFlags := make(map[uint32][]string)
	for cursor.Next(context.Background()) {
		var message Message
		err = cursor.Decode(&message)
		if err != nil {
			return nil, err
		}
		newFlags[message.Uid] = message.Flags
	}

	return newFlags, nil
}

func (repo *MessageRepository) AcquireRecents(mailboxId string) (map[uint32]struct{}, error) {
	recentQuery := bson.D{{
		"mailboxId", mailboxId,
	}, {
		"recent", true,
	}}

	recentProjection := bson.D{{
		"_id", 1,
	}, {
		"uid", 1,
	}}

	cursor, err := repo.conn.Collection(messageCollection).Find(context.Background(), recentQuery, options.Find().SetProjection(recentProjection))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.Background())

	recentMessages := make(map[uint32]struct{})
	for cursor.Next(context.Background()) {
		var message Message
		err = cursor.Decode(&message)
		if err != nil {
			return nil, err
		}

		acquired, err := repo.AcquireRecentMessage(message.ID)
		if err != nil {
			return nil, err
		}

		if acquired {
			recentMessages[message.Uid] = struct{}{}
		} else {
			log.Debugw("recent acquire lost", "mailboxId", mailboxId, "uid", message.Uid)
		}
	}
	return recentMessages, nil
}

func (repo *MessageRepository) AcquireRecentMessage(messageId string) (bool, error) {
	// only update if recent is still true
	messageQuery := bson.D{{
		"_id", messageId,
	}, {
		"recent", true,
	}}

	update := bson.D{{
		"$set", bson.D{{
			"recent", false,
		}},
	}}
	updateResult, err := repo.conn.Collection(messageCollection).UpdateOne(context.Background(), messageQuery, update)
	if err != nil {
		return false, err
	}
	if updateResult.ModifiedCount == 1 {
		return true, nil
	}
	return false, nil
}

func (repo *MessageRepository) ExpungeMailbox(mailboxId string) (int64, error) {
	expungeQuery := bson.D{{
		"mailboxId", mailboxId,
	}, {
		"flags", imap.DeletedFlag,
	}}

	result, err := repo.conn.Collection(messageCollection).DeleteMany(context.Background(), expungeQuery)
	if err != nil {
		return 0, err
	}

	log.Debugw("expunge result", "mailboxId", mailboxId, "count", result.DeletedCount)
	return result.DeletedCount, nil
}

func (repo *MessageRepository) CopyMessage(mailboxSource string, uid uint32, mailboxTarget string, targetUid uint32) (uint32, error) {
	query := bson.D{{
		"mailboxId", mailboxSource,
	}, {
		"uid", uid,
	}}

	result := repo.conn.Collection(messageCollection).FindOne(context.Background(), query)

	var message Message
	err := result.Decode(&message)
	if err != nil {
		return 0, err
	}

	message.MailboxId = mailboxTarget
	message.Uid = targetUid
	message.ID = fmt.Sprintf("%s-%d", mailboxTarget, targetUid)
	message.Recent = true

	ds, err := repo.messageBucket.OpenDownloadStreamByName(fmt.Sprintf("%s-%d", mailboxSource, uid))
	if err != nil {
		return 0, err
	}
	defer ds.Close()

	us, err := repo.messageBucket.OpenUploadStream(fmt.Sprintf("%s-%d", mailboxTarget, targetUid))
	if err != nil {
		return 0, err
	}
	defer us.Close()

	_, err = io.Copy(us, ds)
	if err != nil {
		return 0, err
	}

	// only insert after copy is done, so the change stream only fires when the copy is complete
	_, err = repo.conn.Collection(messageCollection).InsertOne(context.Background(), message)
	if err != nil {
		return 0, err
	}

	return targetUid, nil
}

func mapSearchCriteriaToQuery(criteria *imap.SearchCriteria, uids []uint32, mailboxId string, depth int, invert bool) (bson.D, error) {
	var conditions []bson.D

	if depth == 0 {
		conditions = append(conditions, bson.D{{
			"mailboxId", mailboxId,
		}})
	}

	if criteria.Uid != nil && len(criteria.Uid.Set) != 0 {
		conditions = append(conditions, mapSeqSetToQuery(criteria.Uid, uids[len(uids)-1], invert))
	}

	if criteria.SeqNum != nil && len(criteria.SeqNum.Set) != 0 {
		uidSeq, err := mapSeqNumsToUids(criteria.SeqNum, uids)
		if err != nil {
			return nil, err
		}
		conditions = append(conditions, mapSeqSetToQuery(uidSeq, uids[len(uids)-1], invert))
	}

	if !criteria.Before.IsZero() {
		if invert {
			conditions = append(conditions, bson.D{{
				"internalDate", bson.D{{
					"$not", bson.D{{
						"$lte", criteria.Before,
					}},
				}},
			}})
		} else {
			conditions = append(conditions, bson.D{{
				"internalDate", bson.D{{
					"$lte", criteria.Before,
				}},
			}})
		}
	}

	if !criteria.Since.IsZero() {
		if invert {
			conditions = append(conditions, bson.D{{
				"internalDate", bson.D{{
					"$not", bson.D{{
						"$gte", criteria.Since,
					}},
				}},
			}})
		} else {
			conditions = append(conditions, bson.D{{
				"internalDate", bson.D{{
					"$gte", criteria.Since,
				}},
			}})
		}
	}

	if criteria.Larger != 0 {
		if invert {
			conditions = append(conditions, bson.D{{
				"size", bson.D{{
					"$not", bson.D{{
						"$gte", criteria.Larger,
					}},
				}},
			}})
		} else {
			conditions = append(conditions, bson.D{{
				"size", bson.D{{
					"$gte", criteria.Larger,
				}},
			}})
		}

	}

	if criteria.Smaller != 0 {
		if invert {
			conditions = append(conditions, bson.D{{
				"size", bson.D{{
					"$not", bson.D{{
						"$lte", criteria.Smaller,
					}},
				}},
			}})
		} else {
			conditions = append(conditions, bson.D{{
				"size", bson.D{{
					"$lte", criteria.Smaller,
				}},
			}})
		}
	}

	if criteria.WithFlags != nil {
		if invert {
			conditions = append(conditions, bson.D{{
				"flags", bson.D{{
					"$not", bson.D{{
						"$all", criteria.WithFlags,
					}},
				}},
			}})
		} else {
			conditions = append(conditions, bson.D{{
				"flags", bson.D{{
					"$all", criteria.WithFlags,
				}},
			}})
		}
	}

	if criteria.WithoutFlags != nil {
		if invert {
			conditions = append(conditions, bson.D{{
				"flags", bson.D{{
					"$all", criteria.WithoutFlags,
				}},
			}})
		} else {
			conditions = append(conditions, bson.D{{
				"flags", bson.D{{
					// Yes it's in the right branch
					"$not", bson.D{{
						"$all", criteria.WithoutFlags,
					}},
				}},
			}})
		}
	}

	if criteria.Header != nil {
		headerQueries := make(bson.D, len(criteria.Header))
		i := 0
		for k := range criteria.Header {
			value := criteria.Header.Get(k)

			if invert {
				headerQueries[i] = bson.E{
					Key: fmt.Sprintf("header.%s", strings.ReplaceAll(k, ".", "_")),
					Value: bson.D{{
						"$not", bson.D{{
							"$regex", ".*" + regexp.QuoteMeta(value) + ".*",
						}, {
							"$options", "i",
						}},
					}},
				}
			} else {
				headerQueries[i] = bson.E{
					Key: fmt.Sprintf("header.%s", strings.ReplaceAll(k, ".", "_")),
					Value: bson.D{{
						"$regex", ".*" + regexp.QuoteMeta(value) + ".*",
					}, {
						"$options", "i",
					}},
				}
			}

			i++
		}
		conditions = append(conditions, headerQueries)
	}

	if !criteria.SentBefore.IsZero() {
		if invert {
			conditions = append(conditions, bson.D{{
				"sentDate", bson.D{{
					"$not", bson.D{{
						"$lte", criteria.SentBefore,
					}},
				}},
			}})
		} else {
			conditions = append(conditions, bson.D{{
				"sentDate", bson.D{{
					"$lte", criteria.SentBefore,
				}},
			}})
		}
	}

	if !criteria.SentSince.IsZero() {
		if invert {
			conditions = append(conditions, bson.D{{
				"sentDate", bson.D{{
					"$not", bson.D{{
						"$gte", criteria.SentSince,
					}},
				}},
			}})
		} else {
			conditions = append(conditions, bson.D{{
				"sentDate", bson.D{{
					"$gte", criteria.SentSince,
				}},
			}})
		}
	}

	if criteria.Not != nil {
		for _, c := range criteria.Not {
			subQuery, err := mapSearchCriteriaToQuery(c, uids, mailboxId, depth+1, !invert)
			if err != nil {
				return nil, err
			}
			if subQuery != nil {
				conditions = append(conditions, subQuery)
			}
		}
	}

	if criteria.Or != nil {
		for _, ors := range criteria.Or {
			subQueryA, err := mapSearchCriteriaToQuery(ors[0], uids, mailboxId, depth+1, invert)
			if err != nil {
				return nil, err
			}
			subQueryB, err := mapSearchCriteriaToQuery(ors[1], uids, mailboxId, depth+1, invert)
			if err != nil {
				return nil, err
			}

			if subQueryA == nil && subQueryB == nil {
				continue
			} else if subQueryA == nil && subQueryB != nil {
				conditions = append(conditions, subQueryB)
			} else if subQueryA != nil && subQueryB == nil {
				conditions = append(conditions, subQueryA)
			} else {
				conditions = append(conditions, bson.D{{
					"$or", bson.A{
						subQueryA,
						subQueryB,
					},
				}})
			}
		}
	}

	if criteria.Text != nil {
		log.Warnw("search criteria not implemented", "method", "text")
	}

	if criteria.Body != nil {
		log.Warnw("search criteria not implemented", "method", "body")
	}

	if len(conditions) == 0 {
		return nil, nil
	}

	if len(conditions) == 1 {
		return conditions[0], nil
	}

	if invert {
		return bson.D{{
			"$or", conditions,
		}}, nil
	}
	return bson.D{{
		"$and", conditions,
	}}, nil
}

func mapSeqSetToQuery(seqSet *imap.SeqSet, maxUid uint32, invert bool) bson.D {
	var conditions []bson.D

	for _, seq := range seqSet.Set {
		if seq.Start == 0 && seq.Stop == 0 {
			if invert {
				conditions = append(conditions, bson.D{{
					"uid", bson.D{{
						"$ne", maxUid,
					}},
				}})
			} else {
				conditions = append(conditions, bson.D{{
					"uid", maxUid,
				}})
			}
		} else if seq.Stop == 0 {
			if invert {
				conditions = append(conditions, bson.D{{
					"uid", bson.D{{
						"$not", bson.D{{
							"$gte", seq.Start,
						}},
					}},
				}})
			} else {
				conditions = append(conditions, bson.D{{
					"uid", bson.D{{
						"$gte", seq.Start,
					}},
				}})
			}
		} else {
			if invert {
				conditions = append(conditions, bson.D{{
					"uid", bson.D{{
						"$not", bson.D{{
							"$gte", seq.Start,
						}, {
							"$lte", seq.Stop,
						}},
					}},
				}})
			} else {
				conditions = append(conditions, bson.D{{
					"uid", bson.D{{
						"$gte", seq.Start,
					}, {
						"$lte", seq.Stop,
					}},
				}})
			}
		}
	}

	if len(conditions) == 1 {
		return conditions[0]
	}

	if invert {
		return bson.D{{
			"$and", conditions,
		}}
	}
	return bson.D{{
		"$or", conditions,
	}}
}

func mapSeqNumsToUids(seqSet *imap.SeqSet, uids []uint32) (*imap.SeqSet, error) {
	uidSeqSet := &imap.SeqSet{}
	var currentSeq imap.Seq
	lastUid := uint32(0)
	for _, seq := range seqSet.Set {
		if seq.Start == 0 && seq.Stop == 0 {
			if currentSeq.Start != 0 {
				currentSeq.Stop = lastUid
				uidSeqSet.AddRange(currentSeq.Start, currentSeq.Stop)
				currentSeq = imap.Seq{}
			}
			currentSeq.Start = uids[len(uids)-1]
			currentSeq.Stop = uids[len(uids)-1]
			uidSeqSet.AddRange(currentSeq.Start, currentSeq.Stop)
			currentSeq = imap.Seq{}
			continue
		}
		if seq.Stop == 0 {
			seq.Stop = uint32(len(uids))
		}
		for i := seq.Start; i <= seq.Stop; i++ {
			if i > uint32(len(uids)) {
				return nil, errors.New(fmt.Sprintf("invalid sequence number %d, bigger than mailbox size %d", i, len(uids)))
			}

			currentUid := uids[i-1]
			if currentSeq.Start == 0 {
				currentSeq.Start = currentUid
			} else if lastUid != currentUid-1 {
				currentSeq.Stop = lastUid
				uidSeqSet.AddRange(currentSeq.Start, currentSeq.Stop)
				currentSeq = imap.Seq{
					Start: currentUid,
				}
			}
			lastUid = currentUid
		}
	}
	if currentSeq.Start != 0 {
		currentSeq.Stop = lastUid
		uidSeqSet.AddRange(currentSeq.Start, currentSeq.Stop)
	}
	return uidSeqSet, nil
}
