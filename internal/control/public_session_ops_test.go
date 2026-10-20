package control_test

import (
	context "context"
	sql "database/sql"
	driver "database/sql/driver"
	errors "errors"
	strings "strings"
	testing "testing"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	dberrors "github.com/cocosip/go-oceanbase/dberrors"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestSessionMutationStatementCache(t *testing.T) {
	db := optimizationDB(t, config.ModeMySQL, config.ExecutionOptions{StatementCacheSize: 1}, func(ctx context.Context, p *wire.PacketIO) {
		for id := uint32(7); id <= 8; id++ {
			if !servePrepare(ctx, t, p, testBatchSQL, id, 1, nil) || !testpeer.ServeOK(ctx, t, p, optimizationExecute(id, 1, false), 2) || !testpeer.ReadCommand(ctx, t, p, stmtPacket(25, id)) {
				return
			}
			if id == 7 && !testpeer.ServeOK(ctx, t, p, []byte("\x03SET SESSION sql_mode = 'ANSI_QUOTES'"), 2) {
				return
			}
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
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
	if err = oceanbase.SetSessionVariable(ctx, pinned, "sql_mode", "ANSI_QUOTES"); err != nil {
		t.Fatal(err)
	}
	if _, err = pinned.ExecContext(ctx, testBatchSQL, 1); err != nil {
		t.Fatal(err)
	}
}

func TestSessionOperationEmptyRowset(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadQuery(ctx, t, p, "SELECT DATABASE()") || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 253, 45, 0), []byte{254, 0, 0, 2, 0}, []byte{254, 0, 0, 2, 0}) {
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
	if _, err = oceanbase.CurrentSchema(ctx, pinned); !errors.Is(err, wire.ErrMalformed) {
		t.Fatal(err)
	}
	if err = pinned.Raw(func(raw any) error {
		if raw.(driver.Validator).IsValid() {
			return errors.New("empty scalar helper response left session reusable")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionOperationNoRowset(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeOK(ctx, t, p, []byte("\x03SELECT DATABASE()"), 2) {
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
	if _, err = oceanbase.CurrentSchema(ctx, pinned); !errors.Is(err, wire.ErrMalformed) {
		t.Fatal(err)
	}
	if err = pinned.Raw(func(raw any) error {
		if raw.(driver.Validator).IsValid() {
			return errors.New("unexpected helper OK left session reusable")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionOperationResponse(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(map[bool]string{false: "server-error", true: "extra-result"}[malformed], func(t *testing.T) {
			db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ReadQuery(ctx, t, p, "SELECT DATABASE()") {
					return
				}
				if malformed {
					if !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 253, 45, 0), []byte{254, 0, 0, 2, 0}, []byte{251}, []byte{254, 0, 0, 10, 0}) {
						return
					}
					if _, err := p.ReadMessage(ctx); err == nil {
						t.Error("malformed helper response was reused")
					}
					return
				}
				if !testpeer.SendPackets(ctx, t, p, []byte{255, 21, 4, '#', '4', '2', '0', '0', '0', 'd', 'e', 'n', 'i', 'e', 'd'}) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			ctx := context.Background()
			pinned, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = pinned.Close() }()
			_, err = oceanbase.CurrentSchema(ctx, pinned)
			if malformed {
				if !errors.Is(err, wire.ErrMalformed) {
					t.Fatal(err)
				}
				return
			}
			var server *oceanbase.Error
			if !errors.As(err, &server) || server.Code != 1045 {
				t.Fatal(err)
			}
			if err = pinned.PingContext(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSessionMutationTransaction(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}, {testRollbackSQL, 2}}) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
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
	if err = oceanbase.SetSchema(ctx, pinned, "other"); !errors.Is(err, dberrors.ErrUnsupported) {
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

func TestWarnings(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadQuery(ctx, t, p, "UPDATE t SET v=1") || !testpeer.SendPackets(ctx, t, p, []byte{0, 1, 0, 2, 0, 7, 0}) ||
			!testpeer.ServeOK(ctx, t, p, []byte{14}, 2) || !sessionScalar(ctx, t, p, "SELECT DATABASE()", 2, "app") {
			return
		}
		if !testpeer.ReadQuery(ctx, t, p, "SELECT v") || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 253, 45, 0), []byte{254, 0, 0, 2, 0}, []byte{1, 'x'}, []byte{254, 3, 0, 2, 0}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	e, err := oceanbase.Execute(ctx, pinned, "UPDATE t SET v=1")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	want := oceanbase.WarningSummary{Count: 7, Known: true}
	if got := e.Snapshot().Results[0].Warnings; got != want {
		t.Fatalf("command warnings=%+v", got)
	}
	if err = pinned.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = oceanbase.CurrentSchema(ctx, pinned); err != nil {
		t.Fatal(err)
	}
	if got, err := oceanbase.Warnings(ctx, pinned); err != nil || got != want {
		t.Fatalf("last SQL warnings overwritten by helper: %+v %v", got, err)
	}
	e, err = oceanbase.Execute(ctx, pinned, "SELECT v")
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
	if got, err := oceanbase.Warnings(ctx, pinned); err != nil || got != want {
		t.Fatalf("row warning snapshot=%+v %v", got, err)
	}
}

func TestSessionVariableTypeProbeError(t *testing.T) {
	for _, code := range []uint16{1193, 1142} {
		db := txDB(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
			if !testpeer.ReadQuery(ctx, t, p, testReadSessionVariable) || !testpeer.SendPackets(ctx, t, p, []byte{255, byte(code), byte(code >> 8), '#', 'H', 'Y', '0', '0', '0', 'e'}) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
				return
			}
			p.ResetSequence()
			testpeer.ServeQuit(t, p)
		})
		c := pinnedSession(t, db)
		err := oceanbase.SetSessionVariable(context.Background(), c, testSessionVariable, "600")
		var server *oceanbase.Error
		if !errors.As(err, &server) || server.Code != uint32(code) {
			t.Fatal("variable type probe error swallowed", err)
		}
		if err = c.PingContext(context.Background()); err != nil {
			t.Fatal(err)
		}
		_ = c.Close()
		_ = db.Close()
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
		for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
			want := tc.want
			db := txDB(t, mode, func(ctx context.Context, p *wire.PacketIO) {
				status := byte(2)
				if mode == config.ModeOracle {
					status = 6
				}
				variableQuery := testReadSessionVariable
				if mode == config.ModeOracle {
					variableQuery += testDualSuffix
				}
				if !strings.HasPrefix(tc.want, "'") && !sessionScalarTyped(ctx, t, p, variableQuery, status, 8, "28800") {
					return
				}
				if !testpeer.ServeOK(ctx, t, p, []byte("\x03SET SESSION wait_timeout = "+want), status) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			c := pinnedSession(t, db)
			if err := oceanbase.SetSessionVariable(context.Background(), c, testSessionVariable, tc.value); err != nil {
				t.Errorf("mode=%d value=%q: %v", mode, tc.value, err)
			}
			_ = c.Close()
			_ = db.Close()
		}
	}
}

func TestSessionOperationValidation(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	for _, name := range []string{"", "x;SET @bad=1", "@@global.x"} {
		if err = oceanbase.SetSessionVariable(ctx, pinned, name, "1"); err == nil {
			t.Errorf("accepted variable name %q", name)
		}
	}
	for _, name := range []string{"TIME_ZONE", "CHARACTER_SET_RESULTS", "ncharacter_set_connection", "collation_connection"} {
		if err = oceanbase.SetSessionVariable(ctx, pinned, name, "x"); !errors.Is(err, dberrors.ErrUnsupported) {
			t.Errorf("protected variable %q: %v", name, err)
		}
	}
	for _, name := range []string{"", "invalid\x00schema", "\xff"} {
		if err = oceanbase.SetSchema(ctx, pinned, name); err == nil {
			t.Errorf("accepted schema %q", name)
		}
	}
	if err = oceanbase.SetClientInfo(ctx, pinned, oceanbase.ClientInfo{ApplicationName: "bad\x00value"}); err == nil {
		t.Error("accepted NUL client info")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = oceanbase.CurrentSchema(canceled, pinned); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = oceanbase.CurrentSchema(ctx, nil); err == nil {
		t.Error("accepted nil connection")
	}
	if err = pinned.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSessionMutationIsolation(t *testing.T) {
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		t.Run(map[config.Mode]string{config.ModeMySQL: "mysql-session", config.ModeOracle: "oracle-session"}[mode], func(t *testing.T) {
			status := byte(2)
			if mode == config.ModeOracle {
				status = 6
			}
			db := txDB(t, mode, func(ctx context.Context, p *wire.PacketIO) {
				if mode == config.ModeMySQL {
					if !testpeer.ServeOK(ctx, t, p, []byte("\x02a`b"), status) || !testpeer.ServeOK(ctx, t, p, []byte("\x02other"), status) {
						return
					}
				} else if !testpeer.ServeOK(ctx, t, p, []byte("\x03ALTER SESSION SET CURRENT_SCHEMA = \"a\"\"b\""), status) {
					return
				}
				assignment := "600"
				if !testpeer.ServeOK(ctx, t, p, []byte("\x03SET @ApplicationName = 'svc''x', @ClientUser = NULL, @ClientHostname = 'host'"), status) {
					return
				}
				variableQuery := testReadSessionVariable
				if mode == config.ModeOracle {
					variableQuery += testDualSuffix
				}
				if !sessionScalarTyped(ctx, t, p, variableQuery, status, 8, "28800") {
					return
				}
				if !testpeer.ServeOK(ctx, t, p, []byte("\x03SET SESSION wait_timeout = "+assignment), status) || !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			ctx := context.Background()
			pinned, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			name := "a`b"
			if mode == config.ModeOracle {
				name = "a\"b"
			}
			if err = oceanbase.SetSchema(ctx, pinned, name); err != nil {
				t.Fatal(err)
			}
			err = oceanbase.SetCatalog(ctx, pinned, "other")
			if mode == config.ModeMySQL && err != nil || mode == config.ModeOracle && !errors.Is(err, dberrors.ErrUnsupported) {
				t.Fatal(err)
			}
			if err = oceanbase.SetClientInfo(ctx, pinned, oceanbase.ClientInfo{ApplicationName: "svc'x", ClientHostname: "host"}); err != nil {
				t.Fatal(err)
			}
			if err = oceanbase.SetSessionVariable(ctx, pinned, testSessionVariable, "600"); err != nil {
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

func TestSessionOperations(t *testing.T) {
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		t.Run(map[config.Mode]string{config.ModeMySQL: "mysql-session", config.ModeOracle: "oracle-session"}[mode], func(t *testing.T) {
			status := byte(2)
			schemaQuery := "SELECT DATABASE()"
			clientQuery := "SELECT @ApplicationName, @ClientUser, @ClientHostname"
			variableQuery := testReadSessionVariable
			if mode == config.ModeOracle {
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
				if mode == config.ModeMySQL && !sessionScalar(ctx, t, p, schemaQuery, status, "APP") {
					return
				}
				if !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			ctx := context.Background()
			pinned, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = pinned.Close() }()
			if schema, err := oceanbase.CurrentSchema(ctx, pinned); err != nil || schema != "APP" {
				t.Fatalf("schema=%q error=%v", schema, err)
			}
			if info, err := oceanbase.GetClientInfo(ctx, pinned); err != nil || info != (oceanbase.ClientInfo{ApplicationName: "service", ClientUser: "caller", ClientHostname: "host"}) {
				t.Fatalf("client info=%+v error=%v", info, err)
			}
			if value, err := oceanbase.SessionVariable(ctx, pinned, testSessionVariable); err != nil || value != (sql.NullString{String: "600", Valid: true}) {
				t.Fatalf("variable=%+v error=%v", value, err)
			}
			catalog, err := oceanbase.CurrentCatalog(ctx, pinned)
			if mode == config.ModeMySQL && (err != nil || catalog != "APP") || mode == config.ModeOracle && !errors.Is(err, dberrors.ErrUnsupported) {
				t.Fatalf("catalog=%q error=%v", catalog, err)
			}
			if err := pinned.PingContext(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}
