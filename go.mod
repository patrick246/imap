module github.com/patrick246/imap

go 1.13

require (
	github.com/emersion/go-imap v1.0.6-0.20200802083600-8f00f206af6b
	github.com/emersion/go-message v0.12.0
	github.com/foxcpp/go-imap-backend-tests v0.0.0-20200802191209-a60f58ad32f4
	github.com/foxcpp/go-imap-namespace v0.0.0-20200802091432-08496dd8e0ed
	github.com/fsnotify/fsnotify v1.4.9
	github.com/go-ldap/ldap/v3 v3.2.3
	github.com/spf13/viper v1.7.1
	go.mongodb.org/mongo-driver v1.4.0
	go.uber.org/zap v1.15.0
	golang.org/x/crypto v0.0.0-20200604202706-70a84ac30bf9
)

replace github.com/emersion/go-imap => github.com/foxcpp/go-imap v1.0.0-beta.1.0.20200802083659-cf943ff91d80
