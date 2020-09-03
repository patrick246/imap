package main

import (
	"crypto/tls"
	"fmt"
	"github.com/emersion/go-imap/server"
	"github.com/foxcpp/go-imap-namespace"
	"github.com/patrick246/imap/backend/mongodb"
	"github.com/patrick246/imap/certificatehandler"
	"github.com/patrick246/imap/config"
	"github.com/patrick246/imap/connection/ldap"
	"github.com/patrick246/imap/connection/mongodbConnection"
	"github.com/patrick246/imap/observability/logging"
	"github.com/patrick246/imap/repository"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"os"
	"os/signal"
	"syscall"
)

var log *zap.SugaredLogger

func main() {
	err := config.Init()
	if err != nil {
		fmt.Printf("Could not load config: %v", err)
		panic(err)
	}

	log = logging.CreateLogger("imap-server")

	ldapConn, err := connectToLdap()

	if err != nil {
		log.Fatal("ldap connection error", "error", err)
	}

	uri := config.GetString("mongodb.uri", "mongodb://localhost:27017/mail?replicaSet=rs0")
	dbConnection, err := mongodbConnection.NewConnection(uri)
	if err != nil {
		log.Fatalw("database connection error", "error", err)
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

	tlsConfig, err := buildTlsConfig()
	if err != nil {
		log.Fatalw("error building tls config", "error", err)
	}

	be := mongodb.New(ldapConn, userRepo, mailboxRepo, messageRepo)

	// Create a new server
	imapServer := server.New(be)
	imapServer.AllowInsecureAuth = viper.GetBool("server.imap.allowInsecureAuth")
	imapServer.TLSConfig = tlsConfig
	imapServer.ErrorLog = logging.ImapAdapter{Logger: logging.CreateLogger("go-imap")}
	imapServer.Enable(namespace.NewExtension())
	imapServer.Debug = os.Stdout

	go func() {
		imapServer.Addr = config.GetString("server.imap.address", ":1143")
		log.Infow("starting imap server", "address", imapServer.Addr)
		if err := imapServer.ListenAndServe(); err != nil {
			log.Fatal(err)
		}
	}()

	go func() {
		imapServer.Addr = config.GetString("server.imaps.address", ":1993")
		log.Infow("starting imaps server", "address", imapServer.Addr)
		if err := imapServer.ListenAndServeTLS(); err != nil {
			log.Fatal(err)
		}
	}()

	signals := make(chan os.Signal)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	<-signals
	log.Info("shutting down servers")
	err = imapServer.Close()
	if err != nil {
		log.Fatalw("failed to close server", "error", err)
	}
}

func connectToLdap() (*ldap.Connection, error) {
	var ldapConnInfo ldap.ConnectionInfo
	err := viper.UnmarshalKey("ldap", &ldapConnInfo)
	if err != nil {
		log.Errorw("error loading ldap config, using default", "error", err)
		return nil, err
	}

	log.Infow("connecting to ldap",
		"host", ldapConnInfo.Hostname,
		"port", ldapConnInfo.Port,
		"binddn", ldapConnInfo.Username)

	return ldap.Connect(ldapConnInfo)
}

func buildTlsConfig() (*tls.Config, error) {
	cipherSuiteMap := make(map[string]uint16)

	for _, suite := range tls.CipherSuites() {
		cipherSuiteMap[suite.Name] = suite.ID
	}

	enabledSuitesString := viper.GetStringSlice("server.tls.cipherSuites")

	var enabledSuites []uint16
	for _, enabledSuiteString := range enabledSuitesString {
		enabledSuites = append(enabledSuites, cipherSuiteMap[enabledSuiteString])
	}

	loader, err := certificatehandler.New(
		config.GetString("server.tls.certificate", "tls/tls.crt"),
		config.GetString("server.tls.key", "tls/tls.key"),
	)
	if err != nil {
		return nil, err
	}

	minVersionStr := config.GetString("server.tls.minVersion", "1.2")
	minVersion := uint16(tls.VersionTLS12)
	switch minVersionStr {
	case "1.0":
		minVersion = tls.VersionTLS10
	case "1.1":
		minVersion = tls.VersionTLS11
	case "1.2":
		minVersion = tls.VersionTLS12
	case "1.3":
		minVersion = tls.VersionTLS13
	}

	return &tls.Config{
		GetCertificate:           loader.CertificateLoaderFunc(),
		CipherSuites:             enabledSuites,
		MinVersion:               minVersion,
		NextProtos:               []string{"imap"},
		PreferServerCipherSuites: viper.GetBool("server.tls.serverCipherSuiteOrder"),
	}, nil
}
