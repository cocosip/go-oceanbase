package oceanbase

import (
	"strings"
	"testing"
	"time"
)

func TestDSNStrict(t *testing.T) {
	for _, dsn := range []string{
		"mysql://app:private@localhost/db", "oceanbase://localhost/db", "oceanbase://app:private@localhost/db?unknown=private",
		"oceanbase://app:private@localhost/db?mode=oracle&mode=mysql", "oceanbase://app:private@localhost/db?password=private",
		"oceanbase://app:private@localhost/db?mode=other", "oceanbase://app:private@localhost/db?connectTimeout=-1s",
		"oceanbase://app:private@localhost/db?allowMultiStatements=1", "oceanbase://app:private@localhost/db?mode=auto;user=private",
		"oceanbase://app:private@localhost/db#private", "oceanbase://app:private@localhost/a/b", "oceanbase://app:private@localhost:99999/db",
		"oceanbase://app:private@localhost/db?location=not-a-zone", "oceanbase://app:private@localhost/db?maxMessageBytes=9223372036854775808",
		"oceanbase://app:private@localhost/db?mode=%zz", "oceanbase://app:private@localhost/db?mode", "oceanbase://app:private@localhost:/db",
		"oceanbase://app:private@localhost/db?parseTime=1", "oceanbase://app:private@localhost/db?ncharset=binary",
	} {
		_, err := ParseDSN(dsn)
		if err == nil {
			t.Errorf("accepted malformed DSN (length %d)", len(dsn))
			continue
		}
		if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "oceanbase://") {
			t.Fatalf("DSN error exposed credentials: %v", err)
		}
	}
}

func TestDSNScalarOptions(t *testing.T) {
	cfg, err := ParseDSN("oceanbase://app@localhost/db?parseTime=true&charset=GBK&ncharset=UTF16&location=UTC")
	if err != nil || !cfg.Session.ParseTime || cfg.Session.Charset != "gbk" || cfg.Session.NCharset != testCharsetUTF16 || cfg.Session.Location != time.UTC {
		t.Fatal("scalar options ignored", err)
	}
	cfg, err = ParseDSN("oceanbase://app@localhost/db?parseTime=false")
	if err != nil || cfg.Session.ParseTime {
		t.Fatal("cannot retain textual time", err)
	}
}

func TestDSNInteractive(t *testing.T) {
	for _, value := range []string{dsnTrue, dsnFalse} {
		if _, err := ParseDSN("oceanbase://app@localhost/?interactive=" + value); err != nil {
			t.Fatal("interactive option rejected", err)
		}
	}
	for _, value := range []string{"1", "TRUE", "", "true&interactive=false"} {
		if _, err := ParseDSN("oceanbase://app@localhost/?interactive=" + value); err == nil {
			t.Fatal("invalid interactive option accepted")
		}
	}
}

func TestDSNValues(t *testing.T) {
	cfg, err := ParseDSN("oceanbase://app%40tenant%23cluster:p%40ss%3Aword@[::1]:2883/a%2Fb?mode=mysql&connectTimeout=3s&rowsAffectedMode=changed&resetMode=full&maxMessageBytes=1024&maxLOBBytes=512&fetchSize=4&maxResultItems=8&cleanupTimeout=250ms&allowMultiStatements=true&location=UTC")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.User != "app@tenant#cluster" || cfg.Password != "p@ss:word" || cfg.Addr != "[::1]:2883" || cfg.Database != "a/b" || cfg.Mode != ModeMySQL || cfg.ConnectTimeout != 3*time.Second {
		t.Fatal("DSN escaping or connection fields lost")
	}
	if cfg.RowsAffectedMode != ChangedRows || cfg.ResetMode != ResetFull || !cfg.AllowMultiStatements || cfg.Limits.MaxMessageBytes != 1024 || cfg.Limits.MaxLOBBytes != 512 || cfg.Limits.FetchSize != 4 || cfg.Limits.MaxResultItems != 8 || cfg.Limits.CleanupTimeout != 250*time.Millisecond {
		t.Fatal("DSN options were ignored")
	}
	for _, host := range []string{"localhost", "[::1]", ""} {
		cfg, err = ParseDSN("oceanbase://app@" + host + "/db")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(cfg.Addr, ":2881") {
			t.Fatalf("missing default port: %s", cfg.Addr)
		}
	}
}
