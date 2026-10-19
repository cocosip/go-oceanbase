package oceanbase

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

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const testRoutineOldVersion = "3.2.4.0"

type namedRoutineConnector struct{ c *conn }

func (c namedRoutineConnector) Connect(context.Context) (driver.Conn, error) { return c.c, nil }
func (namedRoutineConnector) Driver() driver.Driver                          { return drv{} }

func serveNamedRoutineMetadata(ctx context.Context, t *testing.T, p *wire.PacketIO, schema, name string, names, directions []string, transform ...func([]any)) bool {
	t.Helper()
	plan, err := metadataPlan(ModeMySQL, testOutVersion, MetadataFilter{Kind: MetadataRoutineParameters, SchemaPattern: exactRoutinePattern(schema), NamePattern: exactRoutinePattern(name)})
	if err != nil {
		t.Error(err)
		return false
	}
	c := &conn{cfg: outputTestConfig(), session: &handshakeSession{mode: ModeMySQL}}
	query, err := c.interpolate(plan.query, plan.args)
	query = strings.Replace(query, " WHERE 1=1", " WHERE 1=1 AND (p.ROUTINE_TYPE='PROCEDURE')", 1)
	if err != nil || !readQuery(ctx, t, p, query) || !sendPackets(ctx, t, p, []byte{15}) {
		return false
	}
	for _, col := range plan.columns {
		if !sendPackets(ctx, t, p, textColumn(col.name, 253, 45, 0)) {
			return false
		}
	}
	if !sendPackets(ctx, t, p, []byte{254, 0, 0, 2, 0}) {
		return false
	}
	for i, parameter := range names {
		row := []any{executionColumnCatalog, schema, name, name, nil, parameter, i + 1, directions[i], typeNameBigInt, nil, nil, nil, nil, nil, 0}
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
		if !sendPackets(ctx, t, p, data) {
			return false
		}
	}
	return sendPackets(ctx, t, p, []byte{254, 0, 0, 2, 0})
}

func TestNamedRoutineClientExec(t *testing.T) {
	c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
		if !serveNamedRoutineMetadata(ctx, t, p, "app", "p", []string{"a", "b"}, []string{"IN", "IN"}) ||
			!serveOK(ctx, t, p, []byte("\x03CALL app.p( 11 , 22 )"), 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	c.session.mode = ModeMySQL
	c.cfg.PrepareMode = PrepareClient
	defer c.discard()
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
	c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
		if !readQuery(ctx, t, p, "SHOW CREATE PROCEDURE `app`.`P`") {
			return
		}
		ddl := "CREATE PROCEDURE p(IN a YEAR(4), IN b BIGINT) SELECT 1"
		if !sendPackets(ctx, t, p, []byte{1}, textColumn("Create Procedure", 253, 45, 0), []byte{254, 0, 0, 2, 0}, append([]byte{byte(len(ddl))}, ddl...), []byte{254, 0, 0, 2, 0}) || !serveOK(ctx, t, p, []byte("\x03CALL app.P( 11 , 22 )"), 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	c.session.mode, c.session.obVersion, c.cfg.PrepareMode = ModeMySQL, testRoutineOldVersion, PrepareClient
	c.session.ok.Status = 2
	defer c.discard()
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
	c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
		if !readQuery(ctx, t, p, "SHOW CREATE PROCEDURE `app`.`p`") ||
			!sendPackets(ctx, t, p, []byte{255, 0x19, 0x05, '#', '4', '2', '0', '0', '0', 'm', 'i', 's', 's', 'i', 'n', 'g'}) ||
			!serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	c.session.mode, c.session.obVersion, c.cfg.PrepareMode = ModeMySQL, testRoutineOldVersion, PrepareClient
	defer c.discard()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := c.ExecContext(ctx, "CALL app.p(?)", []driver.NamedValue{{Name: "a", Ordinal: 1, Value: int64(1)}})
	var server *Error
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
	for _, prepare := range []PrepareMode{PrepareServer, PrepareClient} {
		for _, explicit := range []bool{false, true} {
			for _, query := range []bool{false, true} {
				t.Run(fmt.Sprintf("%d/stmt=%t/query=%t", prepare, explicit, query), func(t *testing.T) {
					const text = "CALL app.p(?,7,?)"
					c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
						if explicit && prepare == PrepareServer && !servePrepare(ctx, t, p, text, 7, 2, nil) {
							return
						}
						for i := 0; i < 2; i++ {
							if !explicit && prepare == PrepareServer && !servePrepare(ctx, t, p, text, 7, 2, nil) {
								return
							}
							if !serveNamedRoutineMetadata(ctx, t, p, "app", "p", []string{"a", "literal", "b"}, []string{"IN", "IN", "IN"}) {
								return
							}
							var payload []byte
							if prepare == PrepareClient {
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
							if !serveOK(ctx, t, p, payload, 2) {
								return
							}
							if !explicit && prepare == PrepareServer && !readCommand(ctx, t, p, stmtPacket(25, 7)) {
								return
							}
						}
						if explicit && prepare == PrepareServer && !readCommand(ctx, t, p, stmtPacket(25, 7)) {
							return
						}
						p.ResetSequence()
						serveQuit(t, p)
					})
					c.session.mode, c.cfg.PrepareMode = ModeMySQL, prepare
					c.session.ok.Status = 2
					db := sql.OpenDB(namedRoutineConnector{c})
					defer c.discard()
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

func TestNamedRoutineOutputs(t *testing.T) {
	for _, prepare := range []PrepareMode{PrepareServer, PrepareClient} {
		t.Run(fmt.Sprint(prepare), func(t *testing.T) {
			c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
				if !serveNamedRoutineMetadata(ctx, t, p, "app", "p", []string{"a", "b", "c"}, []string{"IN", routineDirectionOut, routineDirectionInOut}) {
					return
				}
				p.ResetSequence()
				data, err := p.ReadMessage(ctx)
				matches := regexp.MustCompile(`^\x03SET (@_obgo_[0-9a-f]{32}_0)=NULL,(@_obgo_[0-9a-f]{32}_1)=33$`).FindStringSubmatch(string(data))
				if err != nil || len(matches) != 3 {
					t.Errorf("SET %q: %v", data, err)
					return
				}
				if !sendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) ||
					!serveOK(ctx, t, p, []byte("\x03CALL app.p( 11 , "+matches[1]+" , "+matches[2]+" )"), 2) ||
					!readQuery(ctx, t, p, "SELECT "+matches[1]+","+matches[2]) ||
					!sendPackets(ctx, t, p, []byte{2}, textColumn("b", 253, 45, 0), textColumn("c", 253, 45, 0), []byte{254, 0, 0, 2, 0}, []byte{2, '2', '2', 2, '4', '4'}, []byte{254, 0, 0, 2, 0}) ||
					!serveOK(ctx, t, p, []byte("\x03SET "+matches[1]+"=NULL,"+matches[2]+"=NULL"), 2) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			c.session.mode, c.cfg.PrepareMode = ModeMySQL, prepare
			defer c.discard()
			db := sql.OpenDB(namedRoutineConnector{c})
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

func TestNamedRoutineInvalidGrammar(t *testing.T) {
	for _, text := range []string{selectBindSQL, "CALL p(?); SELECT 1", "CALL p(?, NOW())", "CALL p(? + 1)", "CALL p(?,)", "CALL p(?,@variable)", "CALL p(?,(2))", "CALL p(?,\"x\")", "CALL p(?,/*!1*/2)", "CALL app..p(?)"} {
		if _, err := parseNamedRoutine(text, false); err == nil {
			t.Errorf("accepted %q", text)
		}
	}
	for _, text := range []string{"CALL named_routine(?)", "/*c*/ call `a``b`.`p`(1, ?, 'x,y', NULL, -2e+3); -- c", "CALL p(?, TRUE, .5)"} {
		if _, err := parseNamedRoutine(text, false); err != nil {
			t.Errorf("rejected %q: %v", text, err)
		}
	}
}

func TestNamedRoutineInvalidArguments(t *testing.T) {
	for _, args := range [][]driver.NamedValue{
		{{Name: "a", Value: 1}, {Name: "A", Value: 2}},
		{{Name: "a", Value: 1}, {Value: 2}},
		{{Name: "a", Value: 1}},
	} {
		c := &conn{cfg: outputTestConfig(), session: &handshakeSession{mode: ModeMySQL}}
		plan, _ := buildBindPlan("CALL app.p(?,?)", ModeMySQL)
		if _, err := c.bindCommandArgumentsLocked(context.Background(), plan, args); !errors.Is(err, ErrBind) {
			t.Fatal(err)
		}
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
		{"scalar-out", "a", []string{"a", "b"}, []string{"IN", routineDirectionOut}, false},
		{"scalar-inout", "a", []string{"a", "b"}, []string{"IN", routineDirectionInOut}, false},
		{"output-in", "a", []string{"a", "b"}, []string{"IN", "IN"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
				if !serveNamedRoutineMetadata(ctx, t, p, "app", "p", tc.names, tc.modes) || !serveOK(ctx, t, p, []byte{14}, 2) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			c.session.mode, c.cfg.PrepareMode = ModeMySQL, PrepareClient
			defer c.discard()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var value int64
			var b any = int64(2)
			if tc.output {
				b = sql.Out{Dest: &value}
			}
			_, err := c.ExecContext(ctx, "CALL app.p(?,?)", []driver.NamedValue{{Name: tc.argument, Value: 1}, {Name: "b", Value: b}})
			if !errors.Is(err, ErrBind) || !c.IsValid() || len(c.mu) != 0 || value != 0 {
				t.Fatalf("err=%v valid=%t gate=%d output=%d", err, c.IsValid(), len(c.mu), value)
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
	c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
		for _, schema := range []string{"app_%", "named_schema_next"} {
			if !sessionScalar(ctx, t, p, "SELECT DATABASE()", 2, schema) ||
				!serveNamedRoutineMetadata(ctx, t, p, schema, "named_routine", []string{"a"}, []string{"IN"}) ||
				!serveOK(ctx, t, p, []byte("\x03CALL named_routine( 11 )"), 2) {
				return
			}
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	c.session.mode, c.cfg.PrepareMode = ModeMySQL, PrepareClient
	defer c.discard()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s, err := c.PrepareContext(ctx, "CALL named_routine(?)")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err = s.(driver.StmtExecContext).ExecContext(ctx, []driver.NamedValue{{Name: "a", Value: 11}}); err != nil {
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
			c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
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
				if !sendPackets(ctx, t, p, []byte{255, 0x72, 4, '#', '4', '2', '0', '0', '0', 'd', 'e', 'n', 'i', 'e', 'd'}) || !serveOK(ctx, t, p, []byte{14}, 2) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			c.session.mode, c.cfg.PrepareMode = ModeMySQL, PrepareClient
			defer c.discard()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelRead {
				go func() { <-started; cancel() }()
			}
			_, err := c.ExecContext(ctx, "CALL app.p(?)", []driver.NamedValue{{Name: "a", Value: 11}})
			if cancelRead {
				if err == nil || c.IsValid() || len(c.mu) != 0 {
					t.Fatalf("err=%v valid=%t gate=%d", err, c.IsValid(), len(c.mu))
				}
				return
			}
			var server *Error
			if !errors.As(err, &server) || server.Code != 1138 || !c.IsValid() || len(c.mu) != 0 {
				t.Fatal(err, c.IsValid(), len(c.mu))
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
			c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
				if !serveNamedRoutineMetadata(ctx, t, p, "app", "p", []string{"a"}, []string{"IN"}, tc.transform) {
					return
				}
				if tc.valid && !serveOK(ctx, t, p, []byte("\x03CALL app.p( 11 )"), 2) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			c.session.mode, c.cfg.PrepareMode = ModeMySQL, PrepareClient
			defer c.discard()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := c.ExecContext(ctx, "CALL app.p(?)", []driver.NamedValue{{Name: "a", Value: 11}})
			if tc.valid && err != nil || !tc.valid && !errors.Is(err, ErrBind) {
				t.Fatal(err)
			}
			if err = c.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
