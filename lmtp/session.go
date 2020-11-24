package lmtp

import (
	"bytes"
	"github.com/emersion/go-smtp"
	"github.com/patrick246/imap/repository"
	"io"
	"io/ioutil"
	"time"
)

type Session struct {
	mailboxRepo *repository.MailboxRepository
	messageRepo *repository.MessageRepository
	userRepo    *repository.UserRepository

	from       string
	options    smtp.MailOptions
	recipients []*repository.User
	to         []string
}

func NewSession(mailboxRepo *repository.MailboxRepository, messageRepo *repository.MessageRepository, userRepo *repository.UserRepository) *Session {
	session := &Session{
		mailboxRepo: mailboxRepo,
		messageRepo: messageRepo,
		userRepo:    userRepo,
	}
	session.Reset()
	return session
}

func (s *Session) Reset() {
	s.from = ""
	s.options = smtp.MailOptions{}
	s.recipients = nil
	s.to = nil
}

func (s *Session) Logout() error {
	return nil
}

func (s *Session) Mail(from string, opts smtp.MailOptions) error {
	s.from = from
	s.options = opts
	return nil
}

func (s *Session) Rcpt(to string) error {
	normalizedRecipient, err := normalizeAddress(to)
	if err != nil {
		return err
	}

	recipientUser, err := s.userRepo.FindUserByMail(normalizedRecipient)
	if err != nil {
		return err
	}

	s.recipients = append(s.recipients, recipientUser)
	s.to = append(s.to, to)
	return nil
}

func (s *Session) Data(r io.Reader) error {
	buf, err := ioutil.ReadAll(r)
	if err != nil {
		return err
	}

	log.Debugw("incoming message", "from", s.from, "to", s.recipients, "opts", s.options)

	for _, recipient := range s.recipients {
		err := s.deliverMessageTo(recipient, buf)
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *Session) LMTPData(r io.Reader, status smtp.StatusCollector) error {
	buf, err := ioutil.ReadAll(r)
	if err != nil {
		return err
	}

	log.Debugw("incoming message", "from", s.from, "to", s.recipients, "opts", s.options)

	for i, recipient := range s.recipients {
		err := s.deliverMessageTo(recipient, buf)
		status.SetStatus(s.to[i], err)
	}
	return nil
}

func (s *Session) deliverMessageTo(recipient *repository.User, buf []byte) error {
	uid, err := s.mailboxRepo.AllocateUidById(recipient.PrimaryMailboxId)
	if err != nil {
		return err
	}

	writer, err := s.messageRepo.CreateBodyWriter(recipient.PrimaryMailboxId, uid)
	if err != nil {
		return err
	}
	defer writer.Close()

	teeReader := io.TeeReader(bytes.NewReader(buf), writer)

	message, err := repository.MessageFromBody([]string{}, time.Now(), teeReader, uint32(len(buf)))
	if err != nil {
		return err
	}

	message.MailboxId = recipient.PrimaryMailboxId
	message.Uid = uid

	err = s.messageRepo.Insert(message)
	if err != nil {
		return err
	}

	// Discard remaining bytes, so they get written to GridFS
	_, err = io.Copy(ioutil.Discard, teeReader)
	if err != nil {
		return err
	}

	return nil
}
