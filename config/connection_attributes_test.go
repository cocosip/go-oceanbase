package config

import (
	"errors"
	"strings"
	"testing"
)

func TestConnectionAttributesValidation(t *testing.T) {
	for _, tc := range []struct {
		name, charset string
		attributes    map[string]string
		maxBytes      int64
	}{
		{name: "empty-key", attributes: map[string]string{"": testAttributePrivate}},
		{name: "reserved", attributes: map[string]string{testClientAttributeCapability: testAttributePrivate}},
		{name: "proxy-identity", attributes: map[string]string{"__ob_client_proxy_user_name": testAttributePrivate}},
		{name: "future-reserved", attributes: map[string]string{"_future": testAttributePrivate}},
		{name: "server-capability", attributes: map[string]string{"ob_capability_flag": testAttributePrivate}},
		{name: "nul-key", attributes: map[string]string{"private\x00": testAttributeValue}},
		{name: "nul-value", attributes: map[string]string{testAttributeKey: "private\x00"}},
		{name: "invalid-key", attributes: map[string]string{"private\xff": testAttributeValue}},
		{name: "invalid-value", attributes: map[string]string{testAttributeKey: "private\xff"}},
		{name: "unrepresentable-key", charset: testCharsetASCII, attributes: map[string]string{"应用": testAttributePrivate}},
		{name: "unrepresentable-value", charset: localFilenameCharset, attributes: map[string]string{testAttributeKey: "😀private"}},
		{name: "byte-budget", attributes: map[string]string{testAttributeKey: strings.Repeat("p", 129)}, maxBytes: 128},
		{name: "combined-budget", attributes: map[string]string{testAttributeKey: strings.Repeat("p", 90)}, maxBytes: 128},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Normalize(Config{User: testUser, Session: SessionOptions{Charset: tc.charset}, ConnectionAttributes: tc.attributes, Limits: Limits{MaxMessageBytes: tc.maxBytes}})
			var configErr *ConfigError
			if !errors.As(err, &configErr) || configErr.Field != "ConnectionAttributes" || strings.Contains(err.Error(), testAttributePrivate) {
				t.Fatalf("unsafe attribute validation: %v", err)
			}
		})
	}
}
