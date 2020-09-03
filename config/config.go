package config

import (
	"github.com/spf13/viper"
	"strings"
)

func Init() error {
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath("/etc/imap")
	viper.AddConfigPath("/config")
	viper.AddConfigPath("$HOME/.imap")
	viper.AddConfigPath(".")

	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	viper.AutomaticEnv()

	err := viper.ReadInConfig()
	if _, ok := err.(viper.ConfigFileNotFoundError); ok {
		return nil
	}

	for _, key := range viper.AllKeys() {
		val := viper.Get(key)
		viper.Set(key, val)
	}
	return err
}

func GetString(key, defaultValue string) string {
	value := viper.GetString(key)
	if value == "" {
		return defaultValue
	}
	return value
}
