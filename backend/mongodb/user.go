package mongodb

import (
	"errors"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
	namespace "github.com/foxcpp/go-imap-namespace"
	"github.com/patrick246/imap/backend/mongodb/constants"
	"github.com/patrick246/imap/connection/ldap"
	"github.com/patrick246/imap/repository"
	"go.mongodb.org/mongo-driver/mongo"
	"io"
	"io/ioutil"
	"strings"
	"time"
)

const (
	// namespacePersonalPrefix string = ""
	namespaceOtherPrefix  string = "Other users"
	namespaceSharedPrefix string = "Shared Mailboxes"
)

type ImapUser struct {
	userRepo    *repository.UserRepository
	mailboxRepo *repository.MailboxRepository
	messageRepo *repository.MessageRepository
	userInfo    *ldap.UserInfo
}

func NewUser(
	userRepo *repository.UserRepository,
	mailboxRepo *repository.MailboxRepository,
	messageRepo *repository.MessageRepository,
	userInfo *ldap.UserInfo,
) *ImapUser {
	return &ImapUser{
		userRepo:    userRepo,
		mailboxRepo: mailboxRepo,
		messageRepo: messageRepo,
		userInfo:    userInfo,
	}
}

func (i *ImapUser) Username() string {
	return i.userInfo.Username
}

func (i *ImapUser) Status(mbox string, items []imap.StatusItem) (*imap.MailboxStatus, error) {
	_, mailbox, err := i.GetMailbox(mbox, true, nil)
	if err != nil {
		return nil, err
	}

	mailboxBackend, ok := mailbox.(*ImapMailbox)
	if !ok {
		return nil, errors.New("incompatible mailbox backend returned")
	}

	status, err := mailboxBackend.Status(items)
	_ = mailboxBackend.Close()
	if err != nil {
		return nil, err
	}
	return status, nil
}

func (i *ImapUser) SetSubscribed(mbox string, subscribed bool) error {
	name, owner, err := i.findNameAndOwner(mbox)
	if err != nil {
		return err
	}

	mailbox, err := i.mailboxRepo.FindByNameAndOwner(name, owner)
	if err != nil {
		return err
	}

	_, ok := mailbox.Permissions[i.Username()]
	if !ok {
		log.Warnw("mailbox access without permission", "username", i.userInfo.Username, "name", name)
		return backend.ErrNoSuchMailbox
	}

	return i.mailboxRepo.SetSubscriptionByUser(name, owner, i.Username(), subscribed)
}

func (i *ImapUser) CreateMessage(mbox string, flags []string, date time.Time, body imap.Literal) error {
	name, owner, err := i.findNameAndOwner(mbox)
	if err != nil {
		return err
	}

	mailbox, err := i.mailboxRepo.FindByNameAndOwner(name, owner)
	if err != nil {
		return err
	}

	permission, ok := mailbox.Permissions[i.Username()]
	if !ok {
		log.Warnw("mailbox access without permission", "username", i.userInfo.Username, "name", name)
		return backend.ErrNoSuchMailbox
	}

	if permission == repository.READONLY {
		return errors.New("mailbox is read-only")
	}

	uid, err := i.mailboxRepo.AllocateUid(name, owner)
	if err != nil {
		return err
	}

	gridfsWriter, err := i.messageRepo.CreateBodyWriter(mailbox.Id, uid)
	if err != nil {
		return err
	}
	defer gridfsWriter.Close()

	// Pipe everything to gridfs while parsing the mail for metadata
	teeReader := io.TeeReader(body, gridfsWriter)

	message, err := repository.MessageFromBody(flags, date, teeReader, uint32(body.Len()))
	if err != nil {
		return err
	}

	message.MailboxId = mailbox.Id
	message.Uid = uid

	err = i.messageRepo.Insert(message)
	if err != nil {
		return err
	}

	// Read the remaining bytes, so everything gets written to GridFS
	_, err = io.Copy(ioutil.Discard, teeReader)

	time.Sleep(10 * time.Millisecond)
	return err
}

func (i *ImapUser) ListMailboxes(subscribed bool) ([]imap.MailboxInfo, error) {
	log.Debugw("listing mailboxes start", "subscribed", subscribed, "username", i.userInfo.Username)
	defer log.Debugw("list mailboxes end", "subscribed", subscribed, "username", i.userInfo.Username)

	mailboxes, err := i.mailboxRepo.FindUserMailboxes(i.Username(), subscribed)
	if err != nil {
		log.Errorw("find user mailboxes error", "subscribed", subscribed, "username", i.userInfo.Username)
		return nil, err
	}

	var mailboxInfos []imap.MailboxInfo
	for _, mailbox := range mailboxes {
		var attributes []string
		if mailbox.NewMessages {
			attributes = append(attributes, imap.MarkedAttr)
		}

		var name string
		if mailbox.Owner == i.Username() {
			name = mailbox.Name
		} else {
			name = namespaceOtherPrefix + constants.MailboxPathSeparator + mailbox.Owner + constants.MailboxPathSeparator + mailbox.Name
		}

		mailboxInfo := imap.MailboxInfo{
			Attributes: attributes,
			Delimiter:  constants.MailboxPathSeparator,
			Name:       name,
		}
		mailboxInfos = append(mailboxInfos, mailboxInfo)
	}

	mailboxInfos = append(mailboxInfos, imap.MailboxInfo{
		Attributes: []string{"\\NoSelect"},
		Delimiter:  constants.MailboxPathSeparator,
		Name:       namespaceOtherPrefix,
	})
	mailboxInfos = append(mailboxInfos, imap.MailboxInfo{
		Attributes: []string{"\\NoSelect"},
		Delimiter:  constants.MailboxPathSeparator,
		Name:       namespaceSharedPrefix,
	})

	return mailboxInfos, nil
}

func (i *ImapUser) GetMailbox(name string, readOnly bool, conn backend.Conn) (*imap.MailboxStatus, backend.Mailbox, error) {
	log.Debugw("get mailbox start", "username", i.userInfo.Username, "name", name)
	defer log.Debugw("get mailbox end", "username", i.userInfo.Username, "name", name)

	mailboxName, owner, err := i.findNameAndOwner(name)
	if err != nil {
		return nil, nil, err
	}

	mailbox, err := i.mailboxRepo.FindByNameAndOwner(mailboxName, owner)
	if err != nil {
		return nil, nil, err
	}

	permission, ok := mailbox.Permissions[i.Username()]
	if !ok {
		log.Warnw("mailbox access without permission", "username", i.userInfo.Username, "name", name)
		return nil, nil, backend.ErrNoSuchMailbox
	}

	shouldOpenReadOnly := permission == repository.READONLY || readOnly

	backendMailbox, err := NewMailbox(
		i.userRepo, i.mailboxRepo, i.messageRepo,
		mailbox.Id, mailboxName, owner,
		i.Username(),
		shouldOpenReadOnly,
		conn,
	)
	if err != nil {
		return nil, nil, err
	}

	status, err := backendMailbox.Status([]imap.StatusItem{
		imap.StatusMessages,
		imap.StatusRecent,
		imap.StatusUnseen,
		imap.StatusUidNext,
		imap.StatusUidValidity,
	})
	if err != nil {
		_ = backendMailbox.Close()
		return nil, nil, err
	}

	return status, backendMailbox, nil
}

func (i *ImapUser) CreateMailbox(name string) error {
	if strings.HasPrefix(name, namespaceOtherPrefix+constants.MailboxPathSeparator) {
		log.Warnw("create mailbox in "+namespaceOtherPrefix, "username", i.userInfo.Username, "mailboxName", name)
		return errors.New("can't create new mailboxes for other users")
	}

	mailboxName, owner, err := i.findNameAndOwner(name)
	if err != nil {
		log.Errorw("mailbox name parsing", "name", name, "error", err)
		return err
	}

	parts := strings.Split(mailboxName, constants.MailboxPathSeparator)

	if len(parts) < 1 {
		return errors.New("can't create a zero length mailbox")
	}

	parent := ""

	last := len(parts) - 1
	for index, part := range parts {
		_, err := i.mailboxRepo.CreateMailbox(parent+part, owner, map[string]repository.Permission{
			i.Username(): repository.READWRITE,
		})

		parent = parent + part + constants.MailboxPathSeparator

		if writeErr, ok := err.(mongo.WriteException); ok && writeErr.WriteErrors[0].Code == 11000 {
			if index == last {
				return backend.ErrMailboxAlreadyExists
			} else {
				// Ignore "duplicate key" errors for parent mailboxes, as they can already exist
				continue
			}
		}
		if err != nil {
			log.Errorw("mailbox create error", "name", parent+part, "username", i.userInfo.Username, "owner", owner, "error", err)
			return err
		}

	}
	return nil
}

func (i *ImapUser) DeleteMailbox(name string) error {
	if strings.EqualFold(name, "INBOX") {
		log.Warnw("mailbox inbox delete attempt", "username", i.userInfo.Username)
		return errors.New("deleting inbox is not allowed")
	}

	if strings.HasPrefix(name, namespaceOtherPrefix) {
		log.Warnw("mailbox "+namespaceOtherPrefix+" delete attempt", "username", i.userInfo.Username, "name", name)
		return errors.New("can't delete the mailbox of other users")
	}

	mailboxName, owner, err := i.findNameAndOwner(name)
	if err != nil {
		log.Errorw("mailbox name parsing", "name", name, "error", err)
		return err
	}

	return i.mailboxRepo.DeleteMailbox(mailboxName, owner)
}

func (i *ImapUser) RenameMailbox(existingName, newName string) error {
	return errors.New("not implemented: User.RenameMailbox")
}

func (i *ImapUser) Logout() error {
	return nil
}

func (i *ImapUser) Namespaces() (personal, other, shared []namespace.Namespace, err error) {
	return []namespace.Namespace{{
			Prefix:    "",
			Delimiter: constants.MailboxPathSeparator,
		}},
		[]namespace.Namespace{{
			Prefix:    namespaceOtherPrefix,
			Delimiter: constants.MailboxPathSeparator,
		}},
		[]namespace.Namespace{{
			Prefix:    namespaceSharedPrefix,
			Delimiter: constants.MailboxPathSeparator,
		}}, nil
}

func (i *ImapUser) findNameAndOwner(name string) (mailboxName, owner string, err error) {
	if strings.HasPrefix(name, namespaceSharedPrefix+constants.MailboxPathSeparator) {
		owner = namespaceSharedPrefix
		mailboxName = name[len(owner)+1:]
	} else if strings.HasPrefix(name, namespaceOtherPrefix) {
		parts := strings.SplitN(name, constants.MailboxPathSeparator, 3)
		if len(parts) < 3 {
			return "", "", errors.New("invalid access to " + namespaceOtherPrefix + " mailbox, need to follow format " + namespaceOtherPrefix + "/username/path")
		}
		owner = parts[1]
		mailboxName = parts[2]
	} else {
		owner = i.Username()
		mailboxName = name
	}
	return mailboxName, owner, nil
}
