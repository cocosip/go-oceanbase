package config

import (
	"errors"
	"testing"
)

const testCharsetASCII = "ascii"

func TestSessionCharsetValidation(t *testing.T) {
	for _, name := range []string{"totally-bogus", "utf16", "gbk\x00"} {
		_, err := Normalize(Config{User: testUser, Session: SessionOptions{Charset: name}})
		var invalid *ConfigError
		if !errors.As(err, &invalid) || invalid.Field != "Session.Charset" {
			t.Fatalf("charset %q was not rejected before connecting: %v", name, err)
		}
	}
	for _, name := range []string{"", "UTF8MB4", "utf8", "utf8mb3", "latin1", localFilenameCharset, "gb18030", "gb18030_2022", "hkscs", "hkscs31", testCharsetASCII, "binary"} {
		if _, err := Normalize(Config{User: testUser, Session: SessionOptions{Charset: name}}); err != nil {
			t.Fatalf("supported connection charset %q was rejected: %v", name, err)
		}
	}
	if _, err := ParseDSN("oceanbase://app@localhost/?charset=totally-bogus"); err == nil {
		t.Fatal("invalid DSN charset was accepted")
	}
}
