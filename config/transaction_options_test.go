package config

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestTransactionJavaDSNRejected(t *testing.T) {
	for _, name := range []string{"autocommit", "assureReadOnly", "failOnReadOnly", "oracleChangeReadOnlyToRepeatableRead"} {
		_, err := ParseDSN("oceanbase://user@localhost/?" + name + "=" + url.QueryEscape("private-value"))
		var config *ConfigError
		if !errors.As(err, &config) || strings.Contains(err.Error(), "private-value") {
			t.Fatalf("%s accepted or exposed: %v", name, err)
		}
	}
}
