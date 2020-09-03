package ldap

import (
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/go-ldap/ldap/v3"
	"github.com/patrick246/imap/observability/logging"
)

var log = logging.CreateLogger("ldap-connection")

type ConnectionInfo struct {
	Hostname          string
	Port              uint16
	Username          string
	Password          string
	BaseDN            string
	Scope             int
	UserFilter        string
	UsernameAttribute string
	MailAttribute     string
}

type Connection struct {
	conn *ldap.Conn
	info ConnectionInfo
}

type UserInfo struct {
	Username string
	Mail     string
}

func Connect(info ConnectionInfo) (*Connection, error) {
	l, err := ldap.DialTLS("tcp", fmt.Sprintf("%s:%d", info.Hostname, info.Port), &tls.Config{})
	if err != nil {
		return nil, err
	}

	err = l.Bind(info.Username, info.Password)
	if err != nil {
		l.Close()
		return nil, err
	}
	return &Connection{
		conn: l,
		info: info,
	}, nil
}

func (conn *Connection) Authenticate(username, password string) (bool, error) {
	request := ldap.NewSearchRequest(
		conn.info.BaseDN,
		conn.info.Scope,
		ldap.NeverDerefAliases,
		1,
		0,
		false,
		fmt.Sprintf(conn.findFilterString(), username),
		[]string{"dn"},
		nil,
	)

	result, err := conn.conn.Search(request)
	if err != nil {
		log.Errorw("ldap search error", "error", err, "username", username)
		return false, err
	}

	if len(result.Entries) != 1 {
		log.Warnw("failed authentication, could not find user", "username", username)
		return false, errors.New("could not find user")
	}

	userDn := result.Entries[0].DN
	connInfo := conn.info
	connInfo.Username = userDn
	connInfo.Password = password

	userConn, err := Connect(connInfo)
	if err != nil {
		return false, err
	}
	userConn.conn.Close()
	log.Infow("authenticated user", "username", username, "dn", userDn)
	return true, nil
}

func (conn *Connection) findFilterString() string {
	userFilter := conn.info.UserFilter
	usernameAttribute := conn.info.UsernameAttribute

	var usernameFilter string
	if usernameAttribute == "" {
		usernameFilter = "(uid=%s)"
	} else {
		usernameFilter = fmt.Sprintf("(%s=%%s)", usernameAttribute)
	}

	if userFilter != "" {
		return fmt.Sprintf("(&%s%s)", userFilter, usernameFilter)
	}
	return usernameFilter
}

func (conn *Connection) FindUser(username string) (*UserInfo, error) {
	request := ldap.NewSearchRequest(
		conn.info.BaseDN,
		conn.info.Scope,
		ldap.NeverDerefAliases,
		1,
		0,
		false,
		fmt.Sprintf(conn.findFilterString(), username),
		[]string{conn.info.MailAttribute},
		nil,
	)

	response, err := conn.conn.Search(request)
	if err != nil {
		return nil, err
	}

	if len(response.Entries) != 1 {
		return nil, errors.New("could not find user")
	}

	if len(response.Entries[0].Attributes[0].Values) != 1 {
		return nil, errors.New("user no or more than one mail attribute")
	}

	return &UserInfo{
		Username: username,
		Mail:     response.Entries[0].Attributes[0].Values[0],
	}, nil
}
