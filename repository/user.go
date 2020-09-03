package repository

import (
	"context"
	"github.com/patrick246/imap/connection/ldap"
	"github.com/patrick246/imap/connection/mongodbConnection"
	"github.com/patrick246/imap/observability/logging"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

const userCollection string = "users"

var log = logging.CreateLogger("repository")

type UserRepository struct {
	conn *mongodbConnection.Connection
}

type User struct {
	Name             string `bson:"_id"`
	PrimaryMailboxId string
}

func NewUserRepository(conn *mongodbConnection.Connection) (*UserRepository, error) {
	return &UserRepository{
		conn,
	}, nil
}

func (repo *UserRepository) FindUserByName(name string) (*User, error) {
	query := bson.M{
		"_id": name,
	}

	sr := repo.conn.Collection(userCollection).FindOne(context.Background(), query)

	var result User
	err := sr.Decode(&result)
	return &result, err
}

/**
Method that creates necessary documents for a ldap user if necessary.
Nothing should be modified if the user already exists
*/
func (repo *UserRepository) ProvisionUserFromUserInfo(userInfo *ldap.UserInfo, mailboxRepo *MailboxRepository) (*User, error) {
	doc := bson.M{
		"_id":              userInfo.Username,
		"primaryMailboxId": "",
	}

	_, err := repo.conn.Collection(userCollection).InsertOne(context.Background(), doc)

	// Check for duplicate entry, which is expected here if the user has already been provisioned from LDAP
	if writeException, ok := err.(mongo.WriteException); ok {
		if len(writeException.WriteErrors) == 1 && writeException.WriteErrors[0].Code == 11000 {
			log.Debugw("user already present", "username", userInfo.Username)
			return repo.FindUserByName(userInfo.Username)
		}
	} else if err != nil {
		log.Errorw("user provisioning insert error", "username", userInfo.Username)
		return nil, err
	}

	mailbox, err := mailboxRepo.CreateMailbox("INBOX", userInfo.Username, map[string]Permission{})
	if err != nil {
		log.Errorw("user provisioning create mailbox error", "username", userInfo.Username)
		return nil, err
	}

	updateFilter := bson.D{{
		"_id", userInfo.Username,
	}}

	mailboxUpdate := bson.D{{
		"$set", bson.D{{
			"primaryMailboxId", mailbox.Id,
		}},
	}}

	_, err = repo.conn.Collection(userCollection).UpdateOne(context.Background(), updateFilter, mailboxUpdate)
	if err != nil {
		log.Errorw("user provisioning update user error", "username", userInfo.Username)
		return nil, err
	}

	return repo.FindUserByName(userInfo.Username)
}
