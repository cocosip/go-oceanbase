package call_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/metadata"
)

const testRoutineOldVersion = "3.2.4.0"

func namedRoutineConn(t *testing.T, version string, serve func(context.Context, *wire.PacketIO)) publictest.RawConn {
	t.Helper()
	return publictest.VersionedOutputTestConn(t, version, 0, serve, func(cfg *config.Config) {
		cfg.Mode, cfg.PrepareMode = config.ModeMySQL, config.PrepareClient
	})
}

func serveNamedRoutineMetadata(ctx context.Context, t *testing.T, p *wire.PacketIO, schema, name string, names, directions []string, transform ...func([]any)) bool {
	t.Helper()
	exact := strings.NewReplacer("/", "//", "%", "/%", "_", "/_")
	plan, err := catalog.MySQLPlan(testOutVersion, metadata.MetadataFilter{Kind: metadata.MetadataRoutineParameters, SchemaPattern: exact.Replace(schema), NamePattern: exact.Replace(name)}, true)
	if err != nil {
		t.Error(err)
		return false
	}
	cfg := outputTestConfig()
	physical := session.New(cfg, &session.Negotiated{Mode: config.ModeMySQL})
	op, err := physical.Acquire(ctx)
	if err != nil {
		t.Error(err)
		return false
	}
	defer op.Release()
	query, err := command.Interpolate(op, plan.SQL, plan.Args)
	if err != nil || !testpeer.ReadQuery(ctx, t, p, string(query)) || !testpeer.SendPackets(ctx, t, p, []byte{15}) {
		return false
	}
	for _, col := range plan.Columns {
		if !testpeer.SendPackets(ctx, t, p, testpeer.TextColumn(col.Name, 253, 45, 0)) {
			return false
		}
	}
	if !testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, 2, 0}) {
		return false
	}
	for i, parameter := range names {
		row := []any{"def", schema, name, name, nil, parameter, i + 1, directions[i], valueformat.TypeNameBigInt, nil, nil, nil, nil, nil, 0}
		for _, change := range transform {
			change(row)
		}
		var data []byte
		for _, value := range row {
			if value == nil {
				data = append(data, 251)
			} else {
				text := fmt.Sprint(value)
				data = append(data, byte(len(text)))
				data = append(data, text...)
			}
		}
		if !testpeer.SendPackets(ctx, t, p, data) {
			return false
		}
	}
	return testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, 2, 0})
}

func namedRoutineSchema(ctx context.Context, t *testing.T, p *wire.PacketIO, schema string) bool {
	t.Helper()
	return testpeer.ReadQuery(ctx, t, p, "SELECT DATABASE()") && testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("schema", 253, 45, 0), []byte{254, 0, 0, 2, 0}, append([]byte{byte(len(schema))}, schema...), []byte{254, 0, 0, 2, 0})
}
func TestNamedRoutineClientExec(t *testing.T) {
	c := namedRoutineConn(t, testOutVersion, func(ctx context.Context, p *wire.PacketIO) {
		if !serveNamedRoutineMetadata(ctx, t, p, "app", "p", []string{"a", "b"}, []string{"IN", "IN"}) ||
			!testpeer.ServeOK(ctx, t, p, []byte("\x03CALL app.p( 11 , 22 )"), 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})

	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := c.ExecContext(ctx, "CALL app.p(?,?)", []driver.NamedValue{{Name: "b", Ordinal: 1, Value: int64(22)}, {Name: "A", Ordinal: 2, Value: int64(11)}})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNamedRoutineOldMetadata(t *testing.T) {
	c := namedRoutineConn(t, testRoutineOldVersion, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadQuery(ctx, t, p, "SHOW CREATE PROCEDURE `app`.`P`") {
			return
		}
		ddl := "CREATE PROCEDURE p(IN a YEAR(4), IN b BIGINT) SELECT 1"
		if !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("Create Procedure", 253, 45, 0), []byte{254, 0, 0, 2, 0}, append([]byte{byte(len(ddl))}, ddl...), []byte{254, 0, 0, 2, 0}) || !testpeer.ServeOK(ctx, t, p, []byte("\x03CALL app.P( 11 , 22 )"), 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})

	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := c.ExecContext(ctx, "CALL app.P(?,?)", []driver.NamedValue{{Name: "B", Ordinal: 1, Value: int64(22)}, {Name: "a", Ordinal: 2, Value: int64(11)}})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNamedRoutineOldMissingProcedure(t *testing.T) {
	c := namedRoutineConn(t, testRoutineOldVersion, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadQuery(ctx, t, p, "SHOW CREATE PROCEDURE `app`.`p`") ||
			!testpeer.SendPackets(ctx, t, p, []byte{255, 0x19, 0x05, '#', '4', '2', '0', '0', '0', 'm', 'i', 's', 's', 'i', 'n', 'g'}) ||
			!testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})

	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := c.ExecContext(ctx, "CALL app.p(?)", []driver.NamedValue{{Name: "a", Ordinal: 1, Value: int64(1)}})
	var server *dberrors.Error
	if !errors.As(err, &server) || server.Code != 1305 {
		t.Fatal("missing procedure was replaced by function metadata", err)
	}
	if err = c.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNamedRoutinePublicPaths(t *testing.T) {
	for _, prepare := range []config.PrepareMode{config.PrepareServer, config.PrepareClient} {
		for _, explicit := range []bool{false, true} {
			for _, query := range []bool{false, true} {
				t.Run(fmt.Sprintf("%d/stmt=%t/query=%t", prepare, explicit, query), func(t *testing.T) {
					const text = "CALL app.p(?,7,?)"
					db := publictest.QueryVersionDB(t, testOutVersion, func(ctx context.Context, p *wire.PacketIO) {
						if explicit && prepare == config.PrepareServer && !publictest.ServePrepare(ctx, t, p, text, 7, 2, nil) {
							return
						}
						for i := 0; i < 2; i++ {
							if !explicit && prepare == config.PrepareServer && !publictest.ServePrepare(ctx, t, p, text, 7, 2, nil) {
								return
							}
							if !serveNamedRoutineMetadata(ctx, t, p, "app", "p", []string{"a", "literal", "b"}, []string{"IN", "IN", "IN"}) {
								return
							}
							var payload []byte
							if prepare == config.PrepareClient {
								payload = []byte("\x03CALL app.p( 11 ,7, 22 )")
							} else {
								params := []wire.Parameter{{Type: 8, Data: binary.LittleEndian.AppendUint64(nil, 11)}, {Type: 8, Data: binary.LittleEndian.AppendUint64(nil, 22)}}
								var err error
								payload, err = wire.EncodeStmtExecute(7, params, 4096)
								if err != nil {
									t.Error(err)
									return
								}
							}
							if !testpeer.ServeOK(ctx, t, p, payload, 2) {
								return
							}
							if !explicit && prepare == config.PrepareServer && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
								return
							}
						}
						if explicit && prepare == config.PrepareServer && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
							return
						}
						p.ResetSequence()
						testpeer.ServeQuit(t, p)
					}, func(cfg *config.Config) {
						cfg.Mode, cfg.PrepareMode = config.ModeMySQL, prepare
					})

					defer func() { _ = db.Close() }()
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					var s *sql.Stmt
					if explicit {
						var err error
						s, err = db.PrepareContext(ctx, text)
						if err != nil {
							t.Fatal(err)
						}
					}
					exerciseNamedRoutine(ctx, t, db, s, text, query)
					if explicit {
						if err := s.Close(); err != nil {
							t.Fatal(err)
						}
					}
				})
			}
		}
	}
}

func TestNamedRoutineOutputs(t *testing.T) {
	for _, prepare := range []config.PrepareMode{config.PrepareServer, config.PrepareClient} {
		t.Run(fmt.Sprint(prepare), func(t *testing.T) {
			db := publictest.QueryVersionDB(t, testOutVersion, func(ctx context.Context, p *wire.PacketIO) {
				if !serveNamedRoutineMetadata(ctx, t, p, "app", "p", []string{"a", "b", "c"}, []string{"IN", "OUT", "INOUT"}) {
					return
				}
				p.ResetSequence()
				data, err := p.ReadMessage(ctx)
				matches := regexp.MustCompile(`^\x03SET (@_obgo_[0-9a-f]{32}_0)=NULL,(@_obgo_[0-9a-f]{32}_1)=33$`).FindStringSubmatch(string(data))
				if err != nil || len(matches) != 3 {
					t.Errorf("SET %q: %v", data, err)
					return
				}
				if !testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) ||
					!testpeer.ServeOK(ctx, t, p, []byte("\x03CALL app.p( 11 , "+matches[1]+" , "+matches[2]+" )"), 2) ||
					!testpeer.ReadQuery(ctx, t, p, "SELECT "+matches[1]+","+matches[2]) ||
					!testpeer.SendPackets(ctx, t, p, []byte{2}, testpeer.TextColumn("b", 253, 45, 0), testpeer.TextColumn("c", 253, 45, 0), []byte{254, 0, 0, 2, 0}, []byte{2, '2', '2', 2, '4', '4'}, []byte{254, 0, 0, 2, 0}) ||
					!testpeer.ServeOK(ctx, t, p, []byte("\x03SET "+matches[1]+"=NULL,"+matches[2]+"=NULL"), 2) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			}, func(cfg *config.Config) {
				cfg.Mode, cfg.PrepareMode = config.ModeMySQL, prepare
			})

			defer func() { _ = db.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var b int64
			v := int64(33)
			_, err := db.ExecContext(ctx, "CALL app.p(?,?,?)", sql.Named("c", sql.Out{Dest: &v, In: true}), sql.Named("a", 11), sql.Named("b", sql.Out{Dest: &b}))
			if err != nil || b != 22 || v != 44 {
				t.Fatal(b, v, err)
			}
		})
	}
}

func TestNamedRoutineInvalidArguments(t *testing.T) {
	for i, args := range [][]driver.NamedValue{
		{{Name: "a", Value: 1}, {Name: "A", Value: 2}},
		{{Name: "a", Value: 1}, {Value: 2}},
		{{Name: "a", Value: 1}},
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			c := namedRoutineConn(t, testOutVersion, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = c.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := c.ExecContext(ctx, "CALL app.p(?,?)", args); !errors.Is(err, dberrors.ErrBind) {
				t.Fatal(err)
			}
			if err := c.Ping(ctx); err != nil {
				t.Fatal("invalid arguments reached metadata or retained the operation", err)
			}
		})
	}
}

func TestNamedRoutineInvalidMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, argument string
		names, modes   []string
		output         bool
	}{
		{"unknown-name", "missing_parameter", []string{"a", "b"}, []string{"IN", "IN"}, false},
		{"missing-metadata", "a", []string{"a"}, []string{"IN"}, false},
		{"no-metadata", "a", nil, nil, false},
		{"duplicate-metadata", "a", []string{"a", "A"}, []string{"IN", "IN"}, false},
		{"invalid-direction", "a", []string{"a", "b"}, []string{"IN", "RETURN"}, false},
		{"scalar-out", "a", []string{"a", "b"}, []string{"IN", "OUT"}, false},
		{"scalar-inout", "a", []string{"a", "b"}, []string{"IN", "INOUT"}, false},
		{"output-in", "a", []string{"a", "b"}, []string{"IN", "IN"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := namedRoutineConn(t, testOutVersion, func(ctx context.Context, p *wire.PacketIO) {
				if !serveNamedRoutineMetadata(ctx, t, p, "app", "p", tc.names, tc.modes) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})

			defer func() { _ = c.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var value int64
			var b any = int64(2)
			if tc.output {
				b = sql.Out{Dest: &value}
			}
			_, err := c.ExecContext(ctx, "CALL app.p(?,?)", []driver.NamedValue{{Name: tc.argument, Value: 1}, {Name: "b", Value: b}})
			if !errors.Is(err, dberrors.ErrBind) || !c.IsValid() || value != 0 {
				t.Fatalf("err=%v valid=%t output=%d", err, c.IsValid(), value)
			}
			if err = c.Ping(ctx); err != nil {
				t.Fatal(err)
			}
			if err = c.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNamedRoutineSchemaRefresh(t *testing.T) {
	c := namedRoutineConn(t, testOutVersion, func(ctx context.Context, p *wire.PacketIO) {
		for _, schema := range []string{"app_%", "named_schema_next"} {
			if !namedRoutineSchema(ctx, t, p, schema) ||
				!serveNamedRoutineMetadata(ctx, t, p, schema, "named_routine", []string{"a"}, []string{"IN"}) ||
				!testpeer.ServeOK(ctx, t, p, []byte("\x03CALL named_routine( 11 )"), 2) {
				return
			}
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})

	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s, err := c.PrepareContext(ctx, "CALL named_routine(?)")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err = s.(driver.StmtExecContext).ExecContext(ctx, []driver.NamedValue{{Name: "a", Ordinal: 1, Value: int64(11)}}); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNamedRoutineMetadataFailure(t *testing.T) {
	for _, cancelRead := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelRead), func(t *testing.T) {
			started := make(chan struct{})
			c := namedRoutineConn(t, testOutVersion, func(ctx context.Context, p *wire.PacketIO) {
				p.ResetSequence()
				if _, err := p.ReadMessage(ctx); err != nil {
					t.Error(err)
					return
				}
				close(started)
				if cancelRead {
					_, _ = p.ReadMessage(ctx)
					return
				}
				if !testpeer.SendPackets(ctx, t, p, []byte{255, 0x72, 4, '#', '4', '2', '0', '0', '0', 'd', 'e', 'n', 'i', 'e', 'd'}) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})

			defer func() { _ = c.Close() }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelRead {
				go func() { <-started; cancel() }()
			}
			_, err := c.ExecContext(ctx, "CALL app.p(?)", []driver.NamedValue{{Name: "a", Value: 11}})
			if cancelRead {
				if err == nil || c.IsValid() {
					t.Fatalf("err=%v valid=%t", err, c.IsValid())
				}

				gateCtx, gateCancel := context.WithTimeout(context.Background(), time.Second)
				defer gateCancel()
				if pingErr := c.Ping(gateCtx); !errors.Is(pingErr, driver.ErrBadConn) {
					t.Fatal("cancelled metadata retained the operation", pingErr)
				}
				return
			}
			var server *dberrors.Error
			if !errors.As(err, &server) || server.Code != 1138 || !c.IsValid() {
				t.Fatal(err, c.IsValid())
			}
			if err = c.Ping(ctx); err != nil {
				t.Fatal(err)
			}
			if err = c.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNamedRoutineMetadataIdentity(t *testing.T) {
	for _, tc := range []struct {
		name      string
		transform func([]any)
		valid     bool
	}{
		{"routine-case", func(row []any) { row[2] = "P" }, true},
		{"wrong-schema", func(row []any) { row[1] = "metadata_other_schema" }, false},
		{"wrong-routine", func(row []any) { row[2] = "metadata_other_routine" }, false},
		{"wrong-ordinal", func(row []any) { row[6] = 2 }, false},
		{"missing-specific", func(row []any) { row[3] = nil }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := namedRoutineConn(t, testOutVersion, func(ctx context.Context, p *wire.PacketIO) {
				if !serveNamedRoutineMetadata(ctx, t, p, "app", "p", []string{"a"}, []string{"IN"}, tc.transform) {
					return
				}
				if tc.valid && !testpeer.ServeOK(ctx, t, p, []byte("\x03CALL app.p( 11 )"), 2) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})

			defer func() { _ = c.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := c.ExecContext(ctx, "CALL app.p(?)", []driver.NamedValue{{Name: "a", Value: 11}})
			if tc.valid && err != nil || !tc.valid && !errors.Is(err, dberrors.ErrBind) {
				t.Fatal(err)
			}
			if err = c.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func exerciseNamedRoutine(ctx context.Context, t *testing.T, db *sql.DB, s *sql.Stmt, text string, query bool) {
	t.Helper()
	for i := 0; i < 2; i++ {
		args := []any{sql.Named("B", int64(22)), sql.Named("a", int64(11))}
		var err error
		if query {
			var rows *sql.Rows
			if s != nil {
				rows, err = s.QueryContext(ctx, args...)
			} else {
				rows, err = db.QueryContext(ctx, text, args...)
			}
			if err == nil {
				err = rows.Close()
			}
		} else if s != nil {
			_, err = s.ExecContext(ctx, args...)
		} else {
			_, err = db.ExecContext(ctx, text, args...)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}
