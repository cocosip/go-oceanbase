package oceanbase

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func sessionScalar(ctx context.Context, t *testing.T, p *wire.PacketIO, query string, status byte, values ...string) bool {
	t.Helper()
	if !readQuery(ctx, t, p, query) || !sendPackets(ctx, t, p, []byte{byte(len(values))}) {
		return false
	}
	for range values {
		if !sendPackets(ctx, t, p, textColumn("v", 253, 45, 0)) {
			return false
		}
	}
	row := []byte{}
	for _, v := range values {
		row = append(row, byte(len(v)))
		row = append(row, v...)
	}
	return sendPackets(ctx, t, p, []byte{254, 0, 0, status, 0}, row, []byte{254, 0, 0, status, 0})
}

func TestSessionOperations(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		t.Run(map[Mode]string{ModeMySQL: "mysql-session", ModeOracle: "oracle-session"}[mode], func(t *testing.T) {
			status := byte(2)
			schemaQuery := "SELECT DATABASE()"
			clientQuery := "SELECT @ApplicationName, @ClientUser, @ClientHostname"
			variableQuery := "SELECT @@SESSION.wait_timeout"
			if mode == ModeOracle {
				status = 6
				schemaQuery = "SELECT SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') FROM dual"
				clientQuery = "SELECT @ApplicationName, @ClientUser, @ClientHostname FROM dual"
				variableQuery = "SELECT @@SESSION.wait_timeout FROM dual"
			}
			db := txDB(t, mode, func(ctx context.Context, p *wire.PacketIO) {
				if !sessionScalar(ctx, t, p, schemaQuery, status, "APP") ||
					!sessionScalar(ctx, t, p, clientQuery, status, "service", "caller", "host") ||
					!sessionScalar(ctx, t, p, variableQuery, status, "600") {
					return
				}
				if mode == ModeMySQL && !sessionScalar(ctx, t, p, schemaQuery, status, "APP") {
					return
				}
				if !serveOK(ctx, t, p, []byte{14}, status) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			ctx := context.Background()
			pinned, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = pinned.Close() }()
			if schema, err := CurrentSchema(ctx, pinned); err != nil || schema != "APP" {
				t.Fatalf("schema=%q error=%v", schema, err)
			}
			if info, err := GetClientInfo(ctx, pinned); err != nil || info != (ClientInfo{"service", "caller", "host"}) {
				t.Fatalf("client info=%+v error=%v", info, err)
			}
			if value, err := SessionVariable(ctx, pinned, testSessionVariable); err != nil || value != (sql.NullString{String: "600", Valid: true}) {
				t.Fatalf("variable=%+v error=%v", value, err)
			}
			catalog, err := CurrentCatalog(ctx, pinned)
			if mode == ModeMySQL && (err != nil || catalog != "APP") || mode == ModeOracle && !errors.Is(err, ErrUnsupported) {
				t.Fatalf("catalog=%q error=%v", catalog, err)
			}
			if err := pinned.PingContext(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSessionMutationIsolation(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		t.Run(map[Mode]string{ModeMySQL: "mysql-session", ModeOracle: "oracle-session"}[mode], func(t *testing.T) {
			status := byte(2)
			if mode == ModeOracle {
				status = 6
			}
			db := txDB(t, mode, func(ctx context.Context, p *wire.PacketIO) {
				if mode == ModeMySQL {
					if !serveOK(ctx, t, p, []byte("\x02a`b"), status) || !serveOK(ctx, t, p, []byte("\x02other"), status) {
						return
					}
				} else if !serveOK(ctx, t, p, []byte("\x03ALTER SESSION SET CURRENT_SCHEMA = \"a\"\"b\""), status) {
					return
				}
				assignment := testQuotedTimeout
				if mode == ModeMySQL {
					assignment = "600"
				}
				if !serveOK(ctx, t, p, []byte("\x03SET @ApplicationName = 'svc''x', @ClientUser = NULL, @ClientHostname = 'host'"), status) ||
					!serveOK(ctx, t, p, []byte("\x03SET SESSION wait_timeout = "+assignment), status) || !serveOK(ctx, t, p, []byte{14}, status) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			ctx := context.Background()
			pinned, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			name := "a`b"
			if mode == ModeOracle {
				name = "a\"b"
			}
			if err = SetSchema(ctx, pinned, name); err != nil {
				t.Fatal(err)
			}
			err = SetCatalog(ctx, pinned, "other")
			if mode == ModeMySQL && err != nil || mode == ModeOracle && !errors.Is(err, ErrUnsupported) {
				t.Fatal(err)
			}
			if err = SetClientInfo(ctx, pinned, ClientInfo{ApplicationName: "svc'x", ClientHostname: "host"}); err != nil {
				t.Fatal(err)
			}
			if err = SetSessionVariable(ctx, pinned, testSessionVariable, "600"); err != nil {
				t.Fatal(err)
			}
			if err = pinned.PingContext(ctx); err != nil {
				t.Fatal(err)
			}
			if err = pinned.Close(); err != nil {
				t.Fatal(err)
			}
			if db.Stats().Idle != 0 {
				t.Fatal("mutated session was returned to pool")
			}
		})
	}
}

func TestSessionOperationValidation(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	for _, name := range []string{"", "x;SET @bad=1", "@@global.x"} {
		if err = SetSessionVariable(ctx, pinned, name, "1"); err == nil {
			t.Errorf("accepted variable name %q", name)
		}
	}
	for _, name := range []string{"TIME_ZONE", "CHARACTER_SET_RESULTS", "ncharacter_set_connection", "collation_connection"} {
		if err = SetSessionVariable(ctx, pinned, name, "x"); !errors.Is(err, ErrUnsupported) {
			t.Errorf("protected variable %q: %v", name, err)
		}
	}
	for _, name := range []string{"", "invalid\x00schema", "\xff"} {
		if err = SetSchema(ctx, pinned, name); err == nil {
			t.Errorf("accepted schema %q", name)
		}
	}
	if err = SetClientInfo(ctx, pinned, ClientInfo{ApplicationName: "bad\x00value"}); err == nil {
		t.Error("accepted NUL client info")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = CurrentSchema(canceled, pinned); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = CurrentSchema(ctx, nil); err == nil {
		t.Error("accepted nil connection")
	}
	if err = pinned.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSessionVariableIntegerAndStringSQL(t *testing.T) {
	for _, tc := range []struct{ value, want string }{
		{"600", "600"}, {"+00600", "600"}, {"-1", "-1"},
		{"-9223372036854775808", "-9223372036854775808"},
		{testMaxUint64Text, testMaxUint64Text},
		{"18446744073709551616", "'18446744073709551616'"},
		{"", "''"}, {"1; DROP TABLE t", "'1; DROP TABLE t'"},
		{"1' OR '1", "'1'' OR ''1'"}, {"1.5", "'1.5'"},
		{"0x10", "'0x10'"}, {"1e3", "'1e3'"}, {" 1", "' 1'"},
	} {
		for _, mode := range []Mode{ModeMySQL, ModeOracle} {
			c := &conn{session: &handshakeSession{mode: mode, ok: wire.OK{Status: 2}}}
			want := tc.want
			if mode == ModeOracle {
				want = c.quoteSessionValue(tc.value)
			}
			query, _, _, err := c.sessionOperationSQL(&sessionOperationRequest{op: sessionSetVariable, name: testSessionVariable, value: tc.value})
			if err != nil || query != "SET SESSION wait_timeout = "+want {
				t.Errorf("mode=%d value=%q query=%q err=%v", mode, tc.value, query, err)
			}
		}
	}
}

func TestWarnings(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !readQuery(ctx, t, p, "UPDATE t SET v=1") || !sendPackets(ctx, t, p, []byte{0, 1, 0, 2, 0, 7, 0}) ||
			!serveOK(ctx, t, p, []byte{14}, 2) || !sessionScalar(ctx, t, p, "SELECT DATABASE()", 2, "app") {
			return
		}
		if !readQuery(ctx, t, p, "SELECT v") || !sendPackets(ctx, t, p, []byte{1}, textColumn("v", 253, 45, 0), []byte{254, 0, 0, 2, 0}, []byte{1, 'x'}, []byte{254, 3, 0, 2, 0}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	e, err := Execute(ctx, pinned, "UPDATE t SET v=1")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	want := WarningSummary{Count: 7, Known: true}
	if got := e.Snapshot().Results[0].Warnings; got != want {
		t.Fatalf("command warnings=%+v", got)
	}
	if err = pinned.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = CurrentSchema(ctx, pinned); err != nil {
		t.Fatal(err)
	}
	if got, err := Warnings(ctx, pinned); err != nil || got != want {
		t.Fatalf("last SQL warnings overwritten by helper: %+v %v", got, err)
	}
	e, err = Execute(ctx, pinned, "SELECT v")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	want.Count = 3
	if got := e.Snapshot().Results[0].Warnings; got != want {
		t.Fatalf("row warnings=%+v", got)
	}
	if got, err := Warnings(ctx, pinned); err != nil || got != want {
		t.Fatalf("row warning snapshot=%+v %v", got, err)
	}
}

func TestSessionMutationTransaction(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}, {testRollbackSQL, 2}}) || !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pinned.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = SetSchema(ctx, pinned, "other"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("transaction mutation=%v", err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = pinned.Close(); err != nil {
		t.Fatal(err)
	}
	if db.Stats().Idle != 1 {
		t.Fatal("pre-send validation discarded an unchanged session")
	}
	if err = db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSessionOperationResponse(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(map[bool]string{false: "server-error", true: "extra-result"}[malformed], func(t *testing.T) {
			db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				if !readQuery(ctx, t, p, "SELECT DATABASE()") {
					return
				}
				if malformed {
					if !sendPackets(ctx, t, p, []byte{1}, textColumn("v", 253, 45, 0), []byte{254, 0, 0, 2, 0}, []byte{251}, []byte{254, 0, 0, 10, 0}) {
						return
					}
					if _, err := p.ReadMessage(ctx); err == nil {
						t.Error("malformed helper response was reused")
					}
					return
				}
				if !sendPackets(ctx, t, p, []byte{255, 21, 4, '#', '4', '2', '0', '0', '0', 'd', 'e', 'n', 'i', 'e', 'd'}) || !serveOK(ctx, t, p, []byte{14}, 2) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			ctx := context.Background()
			pinned, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = pinned.Close() }()
			_, err = CurrentSchema(ctx, pinned)
			if malformed {
				if !errors.Is(err, wire.ErrMalformed) {
					t.Fatal(err)
				}
				return
			}
			var server *Error
			if !errors.As(err, &server) || server.Code != 1045 {
				t.Fatal(err)
			}
			if err = pinned.PingContext(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSessionOperationQuoting(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, status := range []uint16{2, 514} {
			c := &conn{session: &handshakeSession{mode: mode, ok: wire.OK{Status: status}}}
			query, _, _, err := c.sessionOperationSQL(&sessionOperationRequest{op: sessionSetClientInfo, info: ClientInfo{ApplicationName: "x\\'y"}})
			want := "SET @ApplicationName = 'x\\''y', @ClientUser = NULL, @ClientHostname = NULL"
			if mode == ModeMySQL && status == 2 {
				want = "SET @ApplicationName = 'x\\\\''y', @ClientUser = NULL, @ClientHostname = NULL"
			}
			if err != nil || query != want {
				t.Fatalf("quote=%q want=%q error=%v", query, want, err)
			}
		}
	}
}

func TestSessionOperationNoRowset(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveOK(ctx, t, p, []byte("\x03SELECT DATABASE()"), 2) {
			return
		}
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("unexpected helper OK did not close transport")
		}
	})
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	if _, err = CurrentSchema(ctx, pinned); !errors.Is(err, wire.ErrMalformed) {
		t.Fatal(err)
	}
	if err = pinned.Raw(func(raw any) error {
		if !raw.(*conn).closed.Load() {
			return errors.New("unexpected helper OK left session reusable")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionOperationEmptyRowset(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !readQuery(ctx, t, p, "SELECT DATABASE()") || !sendPackets(ctx, t, p, []byte{1}, textColumn("v", 253, 45, 0), []byte{254, 0, 0, 2, 0}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		_, _ = p.ReadMessage(ctx)
	})
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	if _, err = CurrentSchema(ctx, pinned); !errors.Is(err, wire.ErrMalformed) {
		t.Fatal(err)
	}
	if err = pinned.Raw(func(raw any) error {
		if !raw.(*conn).closed.Load() {
			return errors.New("empty scalar helper response left session reusable")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionMutationStatementCache(t *testing.T) {
	db := optimizationDB(t, ModeMySQL, ExecutionOptions{StatementCacheSize: 1}, func(ctx context.Context, p *wire.PacketIO) {
		for id := uint32(7); id <= 8; id++ {
			if !servePrepare(ctx, t, p, testBatchSQL, id, 1, nil) || !serveOK(ctx, t, p, optimizationExecute(id, 1, false), 2) || !readCommand(ctx, t, p, stmtPacket(25, id)) {
				return
			}
			if id == 7 && !serveOK(ctx, t, p, []byte("\x03SET SESSION sql_mode = 'ANSI_QUOTES'"), 2) {
				return
			}
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	if _, err = pinned.ExecContext(ctx, testBatchSQL, 1); err != nil {
		t.Fatal(err)
	}
	if err = SetSessionVariable(ctx, pinned, "sql_mode", "ANSI_QUOTES"); err != nil {
		t.Fatal(err)
	}
	if _, err = pinned.ExecContext(ctx, testBatchSQL, 1); err != nil {
		t.Fatal(err)
	}
}

const testSessionVariable = "wait_timeout"

const testQuotedTimeout = "'600'"
