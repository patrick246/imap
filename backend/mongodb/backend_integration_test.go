package mongodb

import (
	"context"
	"fmt"
	"github.com/emersion/go-imap/backend"
	backendtests "github.com/foxcpp/go-imap-backend-tests"
	"github.com/patrick246/imap/connection/ldap"
	"github.com/patrick246/imap/connection/mongodbConnection"
	"github.com/patrick246/imap/repository"
	"math/rand"
	"testing"
)

type BackendIntegrationStub struct {
	id          uint32
	conn        *mongodbConnection.Connection
	userRepo    *repository.UserRepository
	mailboxRepo *repository.MailboxRepository
	messageRepo *repository.MessageRepository
}

func (b *BackendIntegrationStub) GetUser(username string) (backend.User, error) {
	userinfo := ldap.UserInfo{
		Username: username,
		Mail:     []string{fmt.Sprintf("%s@example.com", username)},
	}
	user := NewUser(b.userRepo, b.mailboxRepo, b.messageRepo, &userinfo)
	return user, nil
}

func (b *BackendIntegrationStub) CreateUser(username string) error {
	userinfo := ldap.UserInfo{
		Username: username,
		Mail:     []string{fmt.Sprintf("%s@example.com", username)},
	}
	_, err := b.userRepo.ProvisionUserFromUserInfo(&userinfo, b.mailboxRepo)
	return err
}

func Test_BackendIntegrationTest(t *testing.T) {
	backendtests.RunTests(t, func() backendtests.Backend {
		id := rand.Uint32()
		log.Infof("provisioning backend with id %d", id)

		uri := fmt.Sprintf("mongodb://localhost:27017/mail_integration_%d?replicaSet=rs0&w=majority", id)
		dbConnection, err := mongodbConnection.NewConnection(uri)
		if err != nil {
			log.Fatalw("database connection error", "error", err)
		}
		err = dbConnection.Database().Drop(context.Background())
		if err != nil {
			log.Fatalw("drop database at setup", "error", err)
		}

		userRepo, err := repository.NewUserRepository(dbConnection)
		if err != nil {
			log.Fatalw("user repository setup error", "error", err)
		}
		mailboxRepo, err := repository.NewMailboxRepository(dbConnection)
		if err != nil {
			log.Fatalw("mailbox repository setup error", "error", err)
		}
		messageRepo, err := repository.NewMessageRepository(dbConnection)
		if err != nil {
			log.Fatalw("message repository setup error", "error", err)
		}

		return &BackendIntegrationStub{
			id:          id,
			conn:        dbConnection,
			userRepo:    userRepo,
			mailboxRepo: mailboxRepo,
			messageRepo: messageRepo,
		}
	}, func(backend backendtests.Backend) {
		backendIntegration, ok := backend.(*BackendIntegrationStub)
		if !ok {
			t.Log("backend not of custom backend integration stub type")
		}

		err := backendIntegration.conn.Database().Drop(context.Background())
		if err != nil {
			t.Logf("cleanup failed, delete database No. %d manually. Error: %v", backendIntegration.id, err)
		}
	})
}
