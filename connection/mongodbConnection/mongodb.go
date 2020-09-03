package mongodbConnection

import (
	"context"
	"github.com/patrick246/imap/observability/logging"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/x/mongo/driver/connstring"
	"time"
)

type Connection struct {
	Client   *mongo.Client
	database string
}

var log = logging.CreateLogger("mongodb-connection")

func NewConnection(uri string) (*Connection, error) {
	log.Infow("connecting to MongoDB", "uri", uri)
	connstr, err := connstring.Parse(uri)
	if err != nil {
		return nil, err
	}

	err = connstr.Validate()
	if err != nil {
		return nil, err
	}

	log.Debugw("database uri parsing", "database", connstr.Database)

	client, err := mongo.NewClient(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	err = client.Connect(ctx)
	if err != nil {
		return nil, err
	}

	return &Connection{
		Client:   client,
		database: connstr.Database,
	}, nil
}

func (conn *Connection) Collection(collection string) *mongo.Collection {
	return conn.Client.Database(conn.database).Collection(collection)
}

func (conn *Connection) Database() *mongo.Database {
	return conn.Client.Database(conn.database)
}
