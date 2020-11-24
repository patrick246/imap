package repository

import (
	"context"
	"github.com/patrick246/imap/connection/ldap"
	"github.com/patrick246/imap/connection/mongodbConnection"
	"github.com/patrick246/imap/observability/logging"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const userCollection string = "users"

var log = logging.CreateLogger("repository")

type UserRepository struct {
	conn *mongodbConnection.Connection
}

type User struct {
	Name             string   `bson:"_id"`
	Mails            []string `bson:"mails"`
	PrimaryMailboxId string
}

func NewUserRepository(conn *mongodbConnection.Connection) (*UserRepository, error) {
	_, err := conn.Collection(userCollection).Indexes().CreateOne(context.Background(), mongo.IndexModel{
		Keys:    bson.D{{"mails", 1}},
		Options: options.Index().SetUnique(true),
	})
	if err != nil {
		return nil, err
	}

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

func (repo *UserRepository) FindUserByMail(mail string) (*User, error) {
	query := bson.D{{
		"mails", mail,
	}}

	sr := repo.conn.Collection(userCollection).FindOne(context.Background(), query)

	var result User
	err := sr.Decode(&result)
	return &result, err
}

/**
Method that creates necessary documents for a ldap user if necessary.
If the user already exists, bring existing data in line with ldap contents
*/
func (repo *UserRepository) ProvisionUserFromUserInfo(userInfo *ldap.UserInfo, mailboxRepo *MailboxRepository) (*User, error) {
	update := bson.D{{
		"$set", bson.D{{
			"mails", userInfo.Mail,
		}},
	}}

	updateResult, err := repo.conn.Collection(userCollection).UpdateOne(
		context.Background(),
		bson.D{{"_id", userInfo.Username}},
		update,
		options.Update().SetUpsert(true),
	)
	if err != nil {
		log.Errorw("user provisioning insert error", "username", userInfo.Username)
		return nil, err
	}

	if updateResult.UpsertedCount == 0 {
		return repo.FindUserByName(userInfo.Username)
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
