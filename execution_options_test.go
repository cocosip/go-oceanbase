package oceanbase

import (
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestPrepareExecuteGate(t *testing.T) {
	for _, tc := range []struct {
		mode          Mode
		version       string
		enabled, want bool
	}{
		{ModeOracle, "2.2.76", true, true}, {ModeOracle, "2.2.74", true, false},
		{ModeOracle, "", true, false}, {ModeMySQL, testOutVersion, true, false}, {ModeOracle, testOutVersion, false, false},
	} {
		c := &conn{cfg: Config{Execution: ExecutionOptions{PrepareExecute: tc.enabled}}, session: &handshakeSession{mode: tc.mode, obVersion: tc.version}}
		if c.usePrepareExecute() != tc.want {
			t.Fatal(tc)
		}
	}
	for _, o := range []ExecutionOptions{{StatementCacheSize: -1}, {StatementCacheSQLLimit: -1}} {
		if _, err := NewConnector(Config{User: testUser, Execution: o}); err == nil {
			t.Fatal("negative execution limit")
		}
	}
	cfg, err := ParseDSN("oceanbase://u@localhost/?prepareExecute=true&statementCacheSize=10&statementCacheSQLLimit=200&reuseParameterTypes=true&statementChecksum=true")
	if err != nil || !cfg.Execution.PrepareExecute || cfg.Execution.StatementCacheSize != 10 || cfg.Execution.StatementCacheSQLLimit != 200 || !cfg.Execution.ReuseParameterTypes || !cfg.Execution.StatementChecksum {
		t.Fatal(cfg.Execution, err)
	}
}

func TestStatementChecksumGate(t *testing.T) {
	for _, tc := range []struct {
		mode           Mode
		version        string
		combined, want bool
	}{
		{ModeOracle, "4.1.1", false, false}, {ModeOracle, "4.1.2", false, true},
		{ModeOracle, "", false, false}, {ModeMySQL, testOutVersion, false, false},
		{ModeOracle, "2.2.76", true, true},
	} {
		c := &conn{cfg: Config{Execution: ExecutionOptions{StatementChecksum: true}}, session: &handshakeSession{mode: tc.mode, obVersion: tc.version}}
		o := c.executeOptions([]byte("123456789"), tc.combined)
		if o.UseChecksum != tc.want || tc.want && o.SQLChecksum != 0x58e3fa20 {
			t.Fatal(tc, o)
		}
		c.cfg.Execution.StatementChecksum = false
		if o = c.executeOptions(nil, tc.combined); o != (wire.ExecuteOptions{}) {
			t.Fatal("disabled checksum", o)
		}
	}
}
