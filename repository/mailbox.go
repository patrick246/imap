package repository

import (
	"context"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
	"github.com/patrick246/imap/backend/mongodb/constants"
	"github.com/patrick246/imap/connection/mongodbConnection"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"time"
)

const mailboxCollection string = "mailboxes"

type Permission string

const (
	READONLY  Permission = "r"
	READWRITE Permission = "rw"
)

type MailboxRepository struct {
	conn *mongodbConnection.Connection
}

type Mailbox struct {
	Id          string `bson:"_id"`
	Name        string
	Owner       string
	NewMessages bool
	NextUid     uint32
	UidValidity uint32

	Permissions  map[string]Permission
	SubscribedBy []string
}

func NewMailboxRepository(conn *mongodbConnection.Connection) (*MailboxRepository, error) {
	nameOwnerIndex := mongo.IndexModel{
		Keys:    bson.D{{"owner", 1}, {"name", 1}},
		Options: options.Index().SetUnique(true),
	}

	_, err := conn.Collection(mailboxCollection).Indexes().CreateOne(context.Background(), nameOwnerIndex)
	if err != nil {
		return nil, err
	}

	return &MailboxRepository{
		conn: conn,
	}, nil
}

func (repo *MailboxRepository) CreateMailbox(name, owner string, access map[string]Permission) (*Mailbox, error) {
	// Always give the owner RW permissions on their own mailbox
	access[owner] = READWRITE

	permissions := bson.M{}

	for username, permission := range access {
		permissions[username] = permission
	}

	uidValidity := uint32(time.Now().Unix())
	doc := bson.M{
		"name":        name,
		"newMessages": false,
		"nextUid":     1,
		// We're using the time as unix seconds as a pseudo unique value for a single mailbox
		// This still works when Y2K38 hits
		"uidValidity":  uidValidity,
		"permissions":  permissions,
		"subscribedBy": bson.A{},
		"owner":        owner,
	}

	result, err := repo.conn.Collection(mailboxCollection).InsertOne(context.Background(), doc)
	if err != nil {
		return nil, err
	}

	return &Mailbox{
		Id:           result.InsertedID.(primitive.ObjectID).Hex(),
		Name:         name,
		Owner:        owner,
		NewMessages:  false,
		NextUid:      1,
		UidValidity:  uidValidity,
		Permissions:  access,
		SubscribedBy: nil,
	}, nil
}

func (repo *MailboxRepository) FindUserMailboxes(username string, subscribed bool) ([]Mailbox, error) {
	permissionQuery := bson.D{{
		"permissions." + username, bson.D{{
			"$in", bson.A{READONLY, READWRITE},
		}},
	}}

	var query bson.D
	if subscribed {
		query = bson.D{{
			"$and", bson.A{
				bson.D{{
					"subscribedBy", username,
				}},
				permissionQuery,
			},
		}}
	} else {
		query = permissionQuery
	}

	cursor, err := repo.conn.Collection(mailboxCollection).Find(context.Background(), query)
	if err != nil {
		return nil, err
	}

	var mailboxes []Mailbox
	err = cursor.All(context.Background(), &mailboxes)
	return mailboxes, err
}

func (repo *MailboxRepository) DeleteMailbox(name, owner string) error {

	hasChildren, err := repo.hasChildren(name, owner)
	if err != nil {
		return err
	}

	// ToDo: Remove mailbox contents

	if !hasChildren {
		query := bson.D{{
			"name", name,
		}, {
			"owner", owner,
		}}

		result, err := repo.conn.Collection(mailboxCollection).DeleteOne(context.Background(), query)

		if err != nil {
			return err
		}

		if result.DeletedCount != 1 {
			return backend.ErrNoSuchMailbox
		}
	}

	return nil
}

func (repo *MailboxRepository) FindByNameAndOwner(name, owner string) (*Mailbox, error) {
	query := bson.D{{
		"owner", owner,
	}, {
		"name", name,
	}}

	result := repo.conn.Collection(mailboxCollection).FindOne(context.Background(), query)

	var mailbox Mailbox
	err := result.Decode(&mailbox)
	if err != nil && err == mongo.ErrNoDocuments {
		return nil, backend.ErrNoSuchMailbox
	} else if err != nil {
		return nil, err
	}

	return &mailbox, nil
}

func (repo *MailboxRepository) SetSubscriptionByUser(name, owner, user string, state bool) error {
	query := bson.D{{
		"owner", owner,
	}, {
		"name", name,
	}}

	var operation string
	if state {
		operation = "$addToSet"
	} else {
		operation = "$pull"
	}

	update := bson.D{{
		operation, bson.D{{
			"subscribedBy", user,
		}},
	}}

	_, err := repo.conn.Collection(mailboxCollection).UpdateOne(context.Background(), query, update)
	if err != nil {
		log.Errorw("subscribe update error", "error", err, "name", name, "owner", owner, "username", user)
		return err
	}

	return nil
}

func (repo *MailboxRepository) AllocateUid(name, owner string) (uint32, error) {
	mailboxQuery := bson.D{{
		"owner", owner,
	}, {
		"name", name,
	}}

	update := bson.D{{
		"$inc", bson.D{{
			"nextUid", 1,
		}},
	}}

	projection := bson.D{{
		"nextUid", 1,
	}}

	updateOptions := options.FindOneAndUpdate().SetReturnDocument(options.Before).SetProjection(projection)
	result := repo.conn.Collection(mailboxCollection).FindOneAndUpdate(context.Background(), mailboxQuery, update, updateOptions)

	var mailbox Mailbox
	err := result.Decode(&mailbox)
	return mailbox.NextUid, err
}

func (i *MailboxRepository) FindMailboxFlags(id string) (map[string]struct{}, error) {
	query := bson.D{{
		"_id", bson.D{{
			"$gt", id,
		}, {
			"$lt", id + "\xFF",
		}},
	}}

	results, err := i.conn.Collection(messageCollection).Distinct(context.Background(), "flags", query)
	if err != nil {
		return nil, err
	}

	flags := make(map[string]struct{})
	for _, r := range results {
		if f, ok := r.(string); ok {
			flags[f] = struct{}{}
		}
	}
	return flags, nil
}

func (i *MailboxRepository) CountUnseenMessages(id string) (int64, error) {
	query := bson.D{{
		"mailboxId", id,
	}, {
		"flags", bson.D{{
			"$ne", imap.SeenFlag,
		}},
	}}

	return i.conn.Collection(messageCollection).CountDocuments(context.Background(), query)
}

func (i *MailboxRepository) FindFirstUnseenMessageUid(mailboxId string) (uint32, error) {
	query := bson.D{{
		"mailboxId", mailboxId,
	}, {
		"flags", bson.D{{
			"$ne", imap.SeenFlag,
		}},
	}}

	result := i.conn.Collection(messageCollection).FindOne(context.Background(), query, options.FindOne().SetSort(bson.D{{
		"uid", 1,
	}}).SetProjection(bson.D{{
		"uid", 1,
	}}))

	if result.Err() == mongo.ErrNoDocuments {
		return 0, nil
	}

	var message Message
	err := result.Decode(&message)
	if err != nil {
		return 0, err
	}

	return message.Uid, nil
}

/*
	Determines if the mailbox has any children, this causes different behavior when renaming or deleting

	if an error is returned, the boolean return value can NOT be used
*/
func (repo *MailboxRepository) hasChildren(name, owner string) (bool, error) {
	query := bson.D{{
		"name", bson.D{{
			"$gte", name + constants.MailboxPathSeparator,
		}, {
			"$lt", name + constants.MailboxPathSeparator + "\xFF",
		}},
	}, {
		"owner", owner,
	}}

	count, err := repo.conn.Collection(mailboxCollection).CountDocuments(context.Background(), query)
	if err != nil {
		return false, err
	}

	return count > 0, nil
}