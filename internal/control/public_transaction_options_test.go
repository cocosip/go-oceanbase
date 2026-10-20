package control_test

import (
	context "context"
	sql "database/sql"
	binary "encoding/binary"
	errors "errors"
	net "net"
	atomic "sync/atomic"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestTransactionNativeRoleFailure(t *testing.T) {
	for _, role := range []config.EndpointRole{config.RoleReadWrite, config.RoleReadOnly} {
		var addresses []string
		connector, err := oceanbase.NewConnector(config.Config{User: testUser, Routing: config.RoutingOptions{
			Role: role, Endpoints: []config.Endpoint{{Addr: routeFirst, Role: role}, {Addr: routeSecond, Role: 3 - role}, {Addr: "unknown.example:2881"}},
			MaxAttempts: 3, RetryDelay: time.Nanosecond, BlacklistTimeout: time.Nanosecond,
		}, DialContext: func(_ context.Context, network, address string) (net.Conn, error) {
			addresses = append(addresses, address)
			return nil, &net.OpError{Op: routeDialOperation, Net: network, Err: errors.New("refused")}
		}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = connector.Connect(context.Background()); err == nil || len(addresses) != 3 {
			t.Fatalf("role %d: attempts=%v err=%v", role, addresses, err)
		}
		for _, address := range addresses {
			if address != routeFirst {
				t.Fatalf("role %d fell back to another service: %v", role, addresses)
			}
		}
	}
}

func TestTransactionNativeInitialization(t *testing.T) {
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		for _, variable := range []bool{false, true} {
			t.Run(map[config.Mode]string{config.ModeMySQL: transactionMySQLLabel, config.ModeOracle: transactionOracleLabel}[mode]+"/"+map[bool]string{false: "sql", true: "variable"}[variable], func(t *testing.T) {
				ready := byte(2)
				if mode == config.ModeOracle {
					ready = 6
				}
				off := ready &^ 2
				query := testAutocommitOff
				options := config.SessionOptions{InitSQL: []string{query}}
				if variable {
					options = config.SessionOptions{Variables: map[string]string{"autocommit": "0"}}
					query = "SET SESSION autocommit = 0"
				}
				connector, err := oceanbase.NewConnector(config.Config{User: testUser, Mode: mode, Session: options, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
					if !testpeer.ServeConnect(ctx, t, p, ready) || !testpeer.ServeBaseline(ctx, t, p, ready) {
						return
					}
					if variable {
						lookup := "SELECT @@SESSION.autocommit"
						if mode == config.ModeOracle {
							lookup += testDualSuffix
						}
						if !sessionScalarTyped(ctx, t, p, lookup, ready, 8, "1") {
							return
						}
					}
					if !serveTxSteps(ctx, t, p, []txStep{{query, off}, {transactionUTF8Names, off}, {transactionUTCZone, off}}) {
						return
					}
					p.ResetSequence()
					if _, readErr := p.ReadMessage(context.Background()); readErr == nil {
						t.Error("invalid initial transaction state published")
					}
				})})
				if err != nil {
					t.Fatal(err)
				}
				raw, err := connector.Connect(context.Background())
				if raw != nil {
					_ = raw.Close()
				}
				var config *config.ConfigError
				if !errors.As(err, &config) || config.Field != "Session.InitSQL transaction state" {
					t.Fatalf("disabled initial autocommit accepted: %v", err)
				}
			})
		}
	}
}

func TestTransactionNativeAutocommit(t *testing.T) {
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		t.Run(map[config.Mode]string{config.ModeMySQL: transactionMySQLLabel, config.ModeOracle: transactionOracleLabel}[mode], func(t *testing.T) {
			ready := byte(2)
			if mode == config.ModeOracle {
				ready = 6
			}
			off, active := ready&^2, (ready&^2)|1
			db := txDB(t, mode, func(ctx context.Context, p *wire.PacketIO) {
				steps := []txStep{{testAutocommitOff, off}, {transactionPendingWrite, active}}
				if !serveTxSteps(ctx, t, p, steps) || !testpeer.ServeOK(ctx, t, p, []byte{14}, active) {
					return
				}
				// Pool reuse must roll back the pending transaction before enabling autocommit.
				if !serveTxSteps(ctx, t, p, []txStep{{testRollbackSQL, off}, {testAutocommitOn, ready}}) || !testpeer.ServeOK(ctx, t, p, []byte{14}, ready) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			db.SetMaxOpenConns(1)
			defer func() { _ = db.Close() }()
			pinned, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = pinned.Close() }()
			if _, err = pinned.ExecContext(context.Background(), testAutocommitOff); err != nil {
				t.Fatal(err)
			}
			if tx, beginErr := pinned.BeginTx(context.Background(), nil); beginErr == nil {
				_ = tx.Rollback()
				t.Fatal("BeginTx implicitly changed an autocommit-disabled session")
			}
			if _, err = pinned.ExecContext(context.Background(), transactionPendingWrite); err != nil {
				t.Fatal(err)
			}
			if err = pinned.PingContext(context.Background()); err != nil {
				t.Fatal("fixed connection reset before pool return", err)
			}
			if err = pinned.Close(); err != nil {
				t.Fatal(err)
			}
			if err = db.PingContext(context.Background()); err != nil || db.Stats().OpenConnections != 1 {
				t.Fatalf("pool baseline not restored: %v stats=%+v", err, db.Stats())
			}
		})
	}
}

func TestTransactionNativeReadOnly(t *testing.T) {
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		for _, role := range []config.EndpointRole{config.RoleAny, config.RoleReadOnly} {
			t.Run(map[config.Mode]string{config.ModeMySQL: transactionMySQLLabel, config.ModeOracle: transactionOracleLabel}[mode]+"/"+map[config.EndpointRole]string{config.RoleAny: "any", config.RoleReadOnly: "readonly-service"}[role], func(t *testing.T) {
				ready, active := byte(2), byte(3)
				begin := []txStep{{testSerializableSQL, ready}, {transactionMySQLReadOnly, active}}
				end := []txStep{{testRollbackSQL, ready}}
				code := uint16(1792)
				if mode == config.ModeOracle {
					ready, active, code = 6, 5, 16000
					begin = []txStep{{testAutocommitOff, 4}, {transactionOracleReadOnly, active}}
					end = []txStep{{testRollbackSQL, 4}, {testAutocommitOn, ready}}
				}
				var dials atomic.Int32
				dial := testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
					if !testpeer.ServeConnect(ctx, t, p, ready) || !testpeer.ServeBaseline(ctx, t, p, ready) {
						return
					}
					ctx = context.Background()
					if !serveTxSteps(ctx, t, p, begin) || !testpeer.ReadQuery(ctx, t, p, transactionReadOnlyWrite) {
						return
					}
					packet := binary.LittleEndian.AppendUint16([]byte{255}, code)
					packet = append(packet, []byte("#25006read-only transaction")...)
					if !testpeer.SendPackets(ctx, t, p, packet) || !serveTxSteps(ctx, t, p, end) {
						return
					}
					if role == config.RoleAny {
						steps := []txStep{{testMySQLReadWrite, 3}, {testCommitSQL, ready}}
						if mode == config.ModeOracle {
							steps = []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, active}, {testCommitSQL, 4}, {testAutocommitOn, ready}}
						}
						if !serveTxSteps(ctx, t, p, steps) {
							return
						}
					}
					if !testpeer.ServeOK(ctx, t, p, []byte{14}, ready) {
						return
					}
					p.ResetSequence()
					testpeer.ServeQuit(t, p)
				})
				connector, err := oceanbase.NewConnector(config.Config{User: testUser, Mode: mode, Routing: config.RoutingOptions{Role: role}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
					dials.Add(1)
					return dial(ctx, network, address)
				}})
				if err != nil {
					t.Fatal(err)
				}
				db := sql.OpenDB(connector)
				db.SetMaxOpenConns(1)
				defer func() { _ = db.Close() }()
				pinned, err := db.Conn(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = pinned.Close() }()
				if role == config.RoleReadOnly {
					assertReadWriteRejected(t, pinned)
				}
				tx, err := pinned.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelSerializable, ReadOnly: true})
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback() }()
				_, err = tx.ExecContext(context.Background(), transactionReadOnlyWrite)
				var server *oceanbase.Error
				if !errors.As(err, &server) || server.Code != uint32(code) || server.SQLState != "25006" {
					t.Fatalf("read-only error lost: %v", err)
				}
				if err = tx.Rollback(); err != nil {
					t.Fatal(err)
				}
				if role == config.RoleReadOnly {
					assertReadWriteRejected(t, pinned)
				} else {
					writeTx, beginErr := pinned.BeginTx(context.Background(), nil)
					if beginErr != nil {
						t.Fatal(beginErr)
					}
					if err = writeTx.Commit(); err != nil {
						t.Fatal(err)
					}
				}
				if err = pinned.PingContext(context.Background()); err != nil || dials.Load() != 1 {
					t.Fatalf("transaction changed session: dials=%d err=%v", dials.Load(), err)
				}
			})
		}
	}
}
