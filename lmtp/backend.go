package lmtp

import (
	"errors"
	"github.com/emersion/go-smtp"
	"github.com/patrick246/imap/connection/ldap"
	"github.com/patrick246/imap/repository"
	"golang.org/x/crypto/bcrypt"
)

type Backend struct {
	config      Config
	ldap        *ldap.Connection
	mailboxRepo *repository.MailboxRepository
	messageRepo *repository.MessageRepository
	userRepo    *repository.UserRepository
}

func NewBackend(
	lmtpConfig Config,
	ldap *ldap.Connection,
	mailboxRepo *repository.MailboxRepository,
	messageRepo *repository.MessageRepository,
	userRepo *repository.UserRepository,
) (*Backend, error) {
	return &Backend{
		config:      lmtpConfig,
		ldap:        ldap,
		mailboxRepo: mailboxRepo,
		messageRepo: messageRepo,
		userRepo:    userRepo,
	}, nil
}

func (b *Backend) Login(_ *smtp.ConnectionState, username, password string) (smtp.Session, error) {
	if b.config.AuthenticationType == "" || b.config.AuthenticationType == "none" {
		return nil, smtp.ErrAuthUnsupported
	}

	if b.config.AuthenticationType == "static" {
		usernameMatches := username == b.config.StaticAuth.StaticAuthUsername
		passwordCompareError := bcrypt.CompareHashAndPassword([]byte(b.config.StaticAuth.StaticAuthPasswordHash), []byte(password))

		if usernameMatches && passwordCompareError == nil {
			return &Session{}, nil
		}
	}

	if b.config.AuthenticationType == "ldap" {
		authenticated, err := b.ldap.Authenticate(username, password)
		if err != nil {
			return nil, err
		}

		if authenticated {
			return NewSession(b.mailboxRepo, b.messageRepo, b.userRepo), nil
		}
	}

	return nil, errors.New("authentication failed")
}

func (b *Backend) AnonymousLogin(_ *smtp.ConnectionState) (smtp.Session, error) {
	if b.config.AuthenticationType == "" || b.config.AuthenticationType == "none" {
		return NewSession(b.mailboxRepo, b.messageRepo, b.userRepo), nil

	}

	return nil, smtp.ErrAuthRequired
}
