package mongodb

import (
	"errors"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
	"github.com/patrick246/imap/connection/ldap"
	"github.com/patrick246/imap/observability/logging"
	"github.com/patrick246/imap/repository"
)

var log = logging.CreateLogger("backend")

type mongoBackend struct {
	ldap        *ldap.Connection
	userRepo    *repository.UserRepository
	mailboxRepo *repository.MailboxRepository
	messageRepo *repository.MessageRepository
}

func New(
	ldap *ldap.Connection,
	userRepo *repository.UserRepository,
	mailboxRepo *repository.MailboxRepository,
	messageRepo *repository.MessageRepository,
) *mongoBackend {
	return &mongoBackend{
		ldap:        ldap,
		userRepo:    userRepo,
		mailboxRepo: mailboxRepo,
		messageRepo: messageRepo,
	}
}

func (be *mongoBackend) Login(connInfo *imap.ConnInfo, username, password string) (backend.User, error) {
	authenticated, err := be.ldap.Authenticate(username, password)
	if err != nil {
		return nil, err
	}

	if !authenticated {
		return nil, errors.New("user could not be authenticated")
	}

	log.Infow("user authenticated",
		"username", username,
		"remote", connInfo.RemoteAddr.String(),
		"tls", connInfo.TLS != nil)
	userInfo, err := be.ldap.FindUser(username)
	if err != nil {
		return nil, err
	}

	_, err = be.userRepo.ProvisionUserFromUserInfo(userInfo, be.mailboxRepo)
	if err != nil {
		return nil, err
	}

	user := NewUser(be.userRepo, be.mailboxRepo, be.messageRepo, userInfo)

	return user, nil
}
