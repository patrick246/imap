package lmtp

type Config struct {
	// Can be ldap, static or none
	AuthenticationType string
	StaticAuth         StaticAuthConfig
}

type StaticAuthConfig struct {
	StaticAuthUsername     string
	StaticAuthPasswordHash string
}
