package config

import (
	"testing"
)

func TestAttributesSnapshot(t *testing.T) {
	attributes := map[string]string{"application": "billing,worker:1", "应用": "测试", "empty": ""}
	cfg, err := Normalize(Config{User: testUser, Session: SessionOptions{Charset: "gbk"}, ConnectionAttributes: attributes})
	if err != nil {
		t.Fatal(err)
	}
	attributes["application"] = "changed"
	delete(attributes, "应用")
	for range 2 {
		got, err := connectionAttributes(cfg, "")
		if err != nil || got["application"] != "billing,worker:1" || got["\xd3\xa6\xd3\xc3"] != "\xb2\xe2\xca\xd4" || got["__ob_client_attribute_capability_flag"] != "3" {
			t.Fatalf("attributes no longer use the independent configuration snapshot: %v", err)
		}
		got["application"] = "changed"
	}
	if cfg.ConnectionAttributes["应用"] != "测试" {
		t.Fatal("snapshot no longer contains original Go UTF-8 strings")
	}
}
