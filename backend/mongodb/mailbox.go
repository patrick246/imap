package mongodb

import (
	"context"
	"errors"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
	"github.com/patrick246/imap/backend/mongodb/constants"
	"github.com/patrick246/imap/repository"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ImapMailbox struct {
	userRepo    *repository.UserRepository
	mailboxRepo *repository.MailboxRepository
	messageRepo *repository.MessageRepository

	id       string
	name     string
	owner    string
	username string
	readonly bool

	uids           []int
	uidsLock       sync.RWMutex
	listenerCancel context.CancelFunc
	inserts        chan int
	deletes        chan int
	recents        chan int
	recentsMap     map[uint32]struct{}
	recentsMapLock sync.RWMutex

	updateConn backend.Conn
}

func NewMailbox(
	userRepo *repository.UserRepository,
	mailboxRepo *repository.MailboxRepository,
	messageRepo *repository.MessageRepository,
	id string,
	name string,
	owner string,
	username string,
	readonly bool,
	conn backend.Conn,
) (*ImapMailbox, error) {
	mailbox := &ImapMailbox{
		userRepo:    userRepo,
		mailboxRepo: mailboxRepo,
		messageRepo: messageRepo,
		id:          id,
		name:        name,
		owner:       owner,
		username:    username,
		readonly:    readonly,

		uids:     nil,
		uidsLock: sync.RWMutex{},
		inserts:  make(chan int, 1000),
		deletes:  make(chan int, 1000),
		recents:  make(chan int, 1000),

		recentsMap:     make(map[uint32]struct{}),
		recentsMapLock: sync.RWMutex{},

		updateConn: conn,
	}
	mailbox.setupDatabaseListener()
	return mailbox, nil
}

func (i *ImapMailbox) Name() string {
	return i.name
}

func (i *ImapMailbox) Close() error {
	log.Infow("closing mailbox", "owner", i.owner, "name", i.name)
	i.listenerCancel()
	return nil
}

func (i *ImapMailbox) Info() (*imap.MailboxInfo, error) {
	mailbox, err := i.mailboxRepo.FindByNameAndOwner(i.name, i.owner)
	if err != nil {
		return nil, err
	}

	var attributes []string
	if mailbox.NewMessages {
		attributes = append(attributes, imap.MarkedAttr)
	}

	var name string
	if mailbox.Owner == i.username {
		name = mailbox.Name
	} else if mailbox.Owner == namespaceSharedPrefix {
		name = namespaceSharedPrefix + constants.MailboxPathSeparator + mailbox.Name
	} else {
		name = namespaceOtherPrefix + constants.MailboxPathSeparator + mailbox.Owner + constants.MailboxPathSeparator + mailbox.Name
	}

	i.handleMailboxUpdateWithExpunge(0)

	return &imap.MailboxInfo{
		Attributes: attributes,
		Delimiter:  "/",
		Name:       name,
	}, nil
}

func (i *ImapMailbox) Status(items []imap.StatusItem) (*imap.MailboxStatus, error) {
	itemsResponse := make(map[imap.StatusItem]interface{})
	status := &imap.MailboxStatus{
		Name:     i.name,
		ReadOnly: i.readonly,
	}

	mailbox, err := i.mailboxRepo.FindByNameAndOwner(i.name, i.owner)
	if err != nil {
		return nil, err
	}

	flags, permanentflags, err := i.generateFlagsResponse(i.id)
	if err != nil {
		return nil, err
	}
	status.Flags = flags
	status.PermanentFlags = permanentflags

	unseenUid, err := i.mailboxRepo.FindFirstUnseenMessageUid(i.id)
	if err != nil {
		return nil, err
	}

	if unseenUid == 0 {
		status.UnseenSeqNum = 0
	} else {
		i.uidsLock.RLock()
		index := sort.SearchInts(i.uids, int(unseenUid))
		if index < len(i.uids) && i.uids[index] == int(unseenUid) {
			status.UnseenSeqNum = uint32(index + 1)
		}
		i.uidsLock.RUnlock()
	}

	for _, item := range items {
		switch item {
		case imap.StatusMessages:
			itemsResponse[imap.StatusMessages] = struct{}{}
			i.uidsLock.RLock()
			status.Messages = uint32(len(i.uids))
			i.uidsLock.RUnlock()
		case imap.StatusRecent:
			// ToDo: get recent count
			itemsResponse[imap.StatusRecent] = struct{}{}
			i.recentsMapLock.RLock()
			status.Recent = uint32(len(i.recentsMap))
			i.recentsMapLock.RUnlock()
		case imap.StatusUidNext:
			itemsResponse[imap.StatusUidNext] = struct{}{}
			status.UidNext = mailbox.NextUid
		case imap.StatusUidValidity:
			itemsResponse[imap.StatusUidValidity] = struct{}{}
			status.UidValidity = mailbox.UidValidity
		case imap.StatusUnseen:
			num, err := i.mailboxRepo.CountUnseenMessages(mailbox.Id)
			if err != nil {
				return nil, err
			}
			itemsResponse[imap.StatusUnseen] = struct{}{}
			status.Unseen = uint32(num)
		default:
			log.Debugw("unknown status item requested", "statusItem", item)
		}
	}
	status.Items = itemsResponse

	return status, nil
}

func (i *ImapMailbox) ListMessages(uid bool, seqset *imap.SeqSet, items []imap.FetchItem, ch chan<- *imap.Message) error {
	defer close(ch)

	fetchMessage := func(uidVal, sequenceNumber uint32) {
		message, err := i.messageRepo.FetchMessage(i.id, uidVal, sequenceNumber, items)
		if err != nil {
			return
		}

		i.recentsMapLock.RLock()
		_, isRecent := i.recentsMap[message.Uid]
		i.recentsMapLock.RUnlock()

		if isRecent {
			message.Flags = append(message.Flags, imap.RecentFlag)
		}

		ch <- message
	}

	for _, set := range seqset.Set {
		if set.Start == 0 {
			i.uidsLock.RLock()
			if uid {
				set.Start = uint32(i.uids[len(i.uids)-1])
			} else {
				set.Start = uint32(len(i.uids))
			}
			i.uidsLock.RUnlock()
		}

		if set.Stop == 0 {
			i.uidsLock.RLock()
			if uid {
				set.Stop = uint32(i.uids[len(i.uids)-1])
			} else {
				set.Stop = uint32(len(i.uids))
			}
			i.uidsLock.RUnlock()
		}

		for si := set.Start; si <= set.Stop; si++ {
			var uidVal, seqNum uint32
			if uid {
				uidVal = si

				i.uidsLock.RLock()
				index := sort.SearchInts(i.uids, int(si))
				if index >= len(i.uids) || i.uids[index] != int(si) {
					continue
				}
				i.uidsLock.RUnlock()
				seqNum = uint32(index + 1)
			} else {
				seqNum = si

				i.uidsLock.RLock()
				if seqNum > uint32(len(i.uids)) {
					return errors.New("seqnum bigger than mailbox")
				}
				uidVal = uint32(i.uids[seqNum-1])
				i.uidsLock.RUnlock()
			}
			fetchMessage(uidVal, seqNum)
		}
	}

	if !uid {
		i.handleMailboxUpdatesNoExpunge()
	} else {
		i.handleMailboxUpdateWithExpunge(0)
	}

	return nil
}

func (i *ImapMailbox) SearchMessages(uid bool, criteria *imap.SearchCriteria) ([]uint32, error) {
	i.uidsLock.RLock()
	uidCopy := make([]int, len(i.uids))
	copy(uidCopy, i.uids)
	i.uidsLock.RUnlock()

	uidUint := make([]uint32, 0, len(uidCopy))
	for _, v := range i.uids {
		uidUint = append(uidUint, uint32(v))
	}

	uidResult, err := i.messageRepo.FindMessage(criteria, uidUint, i.id)
	if err != nil {
		return nil, err
	}

	if !uid {
		var seqResult []uint32
		for _, v := range uidResult {
			index := sort.SearchInts(uidCopy, int(v))
			if index >= len(uidCopy) || uidCopy[index] != int(v) {
				log.Warnw("unknown uid returned (removed in the meantime?)")
				continue
			}
			seqResult = append(seqResult, uint32(index+1))
		}
		return seqResult, nil
	}

	if !uid {
		i.handleMailboxUpdatesNoExpunge()
	} else {
		i.handleMailboxUpdateWithExpunge(0)
	}

	return uidResult, nil
}

func (i *ImapMailbox) CopyMessages(uid bool, seqset *imap.SeqSet, dest string) error {
	return errors.New("not implemented: Mailbox.CopyMessages")
}

func (i *ImapMailbox) Expunge() error {
	if i.readonly {
		return errors.New("mailbox is read-only")
	}

	n, err := i.messageRepo.ExpungeMailbox(i.id)
	if err != nil {
		return err
	}

	i.handleMailboxUpdateWithExpunge(n)
	return nil
}

func (i *ImapMailbox) Poll(expunge bool) error {
	if expunge {
		i.handleMailboxUpdateWithExpunge(0)
	} else {
		i.handleMailboxUpdatesNoExpunge()
	}
	return nil
}

func (i *ImapMailbox) UpdateMessagesFlags(uid bool, seqset *imap.SeqSet, operation imap.FlagsOp, silent bool, flags []string) error {
	if i.readonly {
		return errors.New("mailbox is read-only")
	}

	var uidCopy []uint32
	i.uidsLock.RLock()
	for _, u := range i.uids {
		uidCopy = append(uidCopy, uint32(u))
	}
	i.uidsLock.RUnlock()

	newFlags, err := i.messageRepo.UpdateFlags(uid, seqset, operation, flags, uidCopy, i.id)
	if err != nil {
		return err
	}

	if !silent {
		for u, f := range newFlags {
			var seqNum uint32
			i.uidsLock.RLock()
			index := sort.SearchInts(i.uids, int(u))
			if i.uids[index] != int(u) {
				// Uid message has no sequence number, skip update for now
				continue
			}
			seqNum = uint32(index + 1)
			i.uidsLock.RUnlock()

			message := imap.NewMessage(seqNum, []imap.FetchItem{imap.FetchFlags})
			message.Flags = f

			i.recentsMapLock.RLock()
			if _, isRecent := i.recentsMap[u]; isRecent {
				message.Flags = append(message.Flags, imap.RecentFlag)
			}
			i.recentsMapLock.RUnlock()

			if uid {
				message.Uid = u
			}
			err = i.updateConn.SendUpdate(&backend.MessageUpdate{Message: message})
			if err != nil {
				return err
			}
		}
	}

	if !uid {
		i.handleMailboxUpdatesNoExpunge()
	} else {
		i.handleMailboxUpdateWithExpunge(0)
	}

	return nil
}

func (i *ImapMailbox) setupDatabaseListener() {
	ctx, cancel := context.WithCancel(context.Background())
	i.listenerCancel = cancel

	events, err := i.messageRepo.WatchMailbox(ctx, i.id)
	if err != nil {
		log.Errorw("failed to create mailbox watch", "error", err)
		return
	}

	uids, err := i.messageRepo.FetchUids(i.id)
	if err != nil {
		log.Errorw("mailbox subscription fetch", "error", err)
		return
	}

	i.uidsLock.Lock()
	i.uids = uids
	i.uidsLock.Unlock()

	recents, err := i.messageRepo.AcquireRecents(i.id)
	if err != nil {
		log.Errorw("mailbox subscription recent", "error", err)
		return
	}

	i.recentsMapLock.Lock()
	i.recentsMap = recents
	i.recentsMapLock.Unlock()

	go func() {
		for {
			select {
			case event := <-events:
				switch event.OperationType {
				case repository.OpInsert:
					i.processInsertEvent(&event)
				case repository.OpDelete:
					i.processDeleteEvent(&event)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (i *ImapMailbox) processInsertEvent(event *repository.ChangeStreamEvent) {
	fullDocument := event.FullDocument
	if fullDocument == nil {
		log.Errorw("inserted document is null", "id", event.DocumentKey.Id)
		return
	}
	uid := int(fullDocument.Uid)

	// Acquire recent status for new messages
	if fullDocument.Recent {
		acquired, err := i.messageRepo.AcquireRecentMessage(fullDocument.ID)
		if err != nil {
			log.Warnw("error acquiring recent status", "error", err)
		}

		if acquired {
			i.recentsMapLock.Lock()
			i.recentsMap[fullDocument.Uid] = struct{}{}
			i.recentsMapLock.Unlock()
			i.recents <- int(fullDocument.Uid)
		}
	}

	i.inserts <- uid
}

func (i *ImapMailbox) processDeleteEvent(event *repository.ChangeStreamEvent) {
	id := event.DocumentKey.Id
	parts := strings.Split(id, "-")
	if len(parts) != 2 {
		log.Errorw("invalid message id", "id", id)
		return
	}

	uid, err := strconv.Atoi(parts[1])
	if err != nil {
		log.Errorw("invalid message uid", "error", err, "uid", parts[1])
		return
	}

	i.deletes <- uid
}

func (i *ImapMailbox) handleMailboxUpdatesNoExpunge() {
	for {
		select {
		case uid := <-i.inserts:
			i.sendInsertUpdate(uid)

		case _ = <-i.recents:
			i.sendRecentUpdate()

		default:
			return
		}
	}
}

func (i *ImapMailbox) handleMailboxUpdateWithExpunge(expectedNum int64) {
	count := int64(0)
	for {
		select {
		case uid := <-i.inserts:
			i.sendInsertUpdate(uid)

		case _ = <-i.recents:
			i.sendRecentUpdate()

		case uid := <-i.deletes:
			i.sendExpungeUpdate(uid)
		default:
			if count < expectedNum {
				time.Sleep(10 * time.Millisecond)
			} else {
				return
			}
		}
		count++
	}
}

func (i *ImapMailbox) sendInsertUpdate(uid int) {
	i.uidsLock.Lock()
	index := sort.SearchInts(i.uids, uid)
	i.uids = insertAt(i.uids, index, uid)
	messageCount := uint32(len(i.uids))
	i.uidsLock.Unlock()

	mailboxStatus := &imap.MailboxStatus{Messages: messageCount}
	mailboxStatus.Items = make(map[imap.StatusItem]interface{})
	mailboxStatus.Items[imap.StatusMessages] = struct{}{}

	err := i.updateConn.SendUpdate(&backend.MailboxUpdate{MailboxStatus: mailboxStatus})
	if err != nil {
		log.Warnw("send update error", "error", err, "event", "insert")
	}
}

func (i *ImapMailbox) sendRecentUpdate() {
	i.recentsMapLock.RLock()
	recentsNum := uint32(len(i.recentsMap))
	i.recentsMapLock.RUnlock()

	mailboxStatus := &imap.MailboxStatus{Recent: recentsNum}
	mailboxStatus.Items = make(map[imap.StatusItem]interface{})
	mailboxStatus.Items[imap.StatusRecent] = struct{}{}

	err := i.updateConn.SendUpdate(&backend.MailboxUpdate{MailboxStatus: mailboxStatus})
	if err != nil {
		log.Warnw("send update error", "error", err, "event", "recent")
	}
}

func (i *ImapMailbox) sendExpungeUpdate(uid int) {
	i.uidsLock.Lock()
	index := sort.SearchInts(i.uids, uid)
	sendExpunge := false
	if i.uids[index] == uid {
		sendExpunge = true
		copy(i.uids[index:], i.uids[index+1:])
		i.uids = i.uids[:len(i.uids)-1]
	}
	i.uidsLock.Unlock()

	i.recentsMapLock.Lock()
	_, isRecent := i.recentsMap[uint32(uid)]
	if isRecent {
		delete(i.recentsMap, uint32(uid))
	}
	i.recentsMapLock.Unlock()

	if sendExpunge {
		err := i.updateConn.SendUpdate(&backend.ExpungeUpdate{SeqNum: uint32(index + 1)})
		if err != nil {
			log.Warnw("send update error", "error", err, "event", "delete")
		}
	}
}

func insertAt(slice []int, index, value int) []int {
	slice = append(slice, 0)
	copy(slice[index+1:], slice[index:])
	slice[index] = value
	return slice
}

func (i *ImapMailbox) generateFlagsResponse(id string) (flags []string, permanentFlags []string, err error) {
	flagMap, err := i.mailboxRepo.FindMailboxFlags(id)
	if err != nil {
		return nil, nil, err
	}

	systemFlags := []string{
		imap.SeenFlag, imap.AnsweredFlag,
		imap.FlaggedFlag, imap.DeletedFlag,
		imap.DraftFlag, imap.RecentFlag,
	}
	for _, sf := range systemFlags {
		flagMap[sf] = struct{}{}
	}

	for f := range flagMap {
		flags = append(flags, f)
	}
	for _, f := range flags {
		if f != imap.RecentFlag {
			permanentFlags = append(permanentFlags, f)
		}
	}

	permanentFlags = append(permanentFlags, imap.TryCreateFlag)

	return flags, permanentFlags, nil
}
