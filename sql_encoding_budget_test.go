package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const sqlBudgetSelect = "SELECT '测测'"

func TestSQLTextEncodedBudget(t *testing.T) {
	for _, tc := range []struct{ name, text, encoded string }{
		{localFilenameCharset, sqlBudgetSelect, "SELECT '\xb2\xe2\xb2\xe2'"},
		{identityCharsetLatin, "SELECT '€€'", "SELECT '\x80\x80'"},
		{testAttributeGB18030, "SELECT '\u0080\u0080'", "SELECT '" + parameterBudgetExpansion + "'"},
		{charsetUTF8MB4, sqlBudgetSelect, sqlBudgetSelect},
	} {
		for _, extra := range []int64{1, 0} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, extra), func(t *testing.T) {
				c := &conn{cfg: Config{Session: SessionOptions{Charset: tc.name}, Limits: Limits{MaxMessageBytes: int64(len(tc.encoded)) + extra}}}
				got, err := c.encodeSQL(tc.text)
				if extra == 0 {
					if !errors.Is(err, wire.ErrMessageTooLarge) {
						t.Fatal("command byte escaped message budget", err)
					}
				} else if err != nil || string(got) != tc.encoded {
					t.Fatalf("encoded SQL=%x want %x: %v", got, tc.encoded, err)
				}
			})
		}
	}
}

func TestClientSQLTextEncodedBudget(t *testing.T) {
	for _, tc := range []struct {
		name, charset, query, want string
		mode                       Mode
		args                       []driver.NamedValue
	}{
		{"spans", localFilenameCharset, "SELECT '测测', ?, '测测', ?", "SELECT '\xb2\xe2\xb2\xe2',  1 , '\xb2\xe2\xb2\xe2',  2 ", ModeMySQL, []driver.NamedValue{{Value: int64(1)}, {Value: int64(2)}}},
		{"tail", localFilenameCharset, "SELECT ?, '测测'", "SELECT  1 , '\xb2\xe2\xb2\xe2'", ModeOracle, []driver.NamedValue{{Value: int64(1)}}},
		{"oracle-literal", localFilenameCharset, selectBindSQL, "SELECT  ('" + strings.Repeat(parameterBudgetGBK, 10) + "''\\'||CHR(0)||'') ", ModeOracle, []driver.NamedValue{{Value: strings.Repeat(parameterBudgetText, 10) + "'\\\x00"}}},
		{"mysql-literal", identityCharsetLatin, selectBindSQL, "SELECT  _latin1 X'" + strings.Repeat("80", 30) + "' ", ModeMySQL, []driver.NamedValue{{Value: strings.Repeat("€", 30)}}},
		{"named", localFilenameCharset, "SELECT :v, '测测', :v", "SELECT  '\xb2\xe2\xb2\xe2' , '\xb2\xe2\xb2\xe2',  '\xb2\xe2\xb2\xe2' ", ModeOracle, []driver.NamedValue{{Name: "v", Value: parameterBudgetText}}},
		{"expansion", testAttributeGB18030, selectBindSQL, "SELECT  '" + parameterBudgetExpansion + "' ", ModeOracle, []driver.NamedValue{{Value: "\u0080\u0080"}}},
	} {
		for _, extra := range []int64{1, 0} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, extra), func(t *testing.T) {
				c := &conn{cfg: Config{Session: SessionOptions{Charset: tc.charset}, Limits: Limits{MaxMessageBytes: int64(len(tc.want)) + extra}}, session: &handshakeSession{mode: tc.mode}}
				got, err := c.interpolate(tc.query, tc.args)
				if extra == 0 {
					if !errors.Is(err, wire.ErrMessageTooLarge) {
						t.Fatal("interpolated SQL exceeded message budget", err)
					}
				} else if err != nil || got != tc.want {
					t.Fatalf("interpolated SQL=%x want %x: %v", got, tc.want, err)
				}
			})
		}
	}
}

func TestSQLBudgetStrictEncoding(t *testing.T) {
	for _, tc := range []struct{ name, text string }{
		{localFilenameCharset, "😀"},
		{charsetUTF8MB4, "\xff"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &conn{cfg: Config{Session: SessionOptions{Charset: tc.name}, Limits: Limits{MaxMessageBytes: 64}}, session: &handshakeSession{mode: ModeOracle}}
			if _, err := c.encodeSQL("SELECT '" + tc.text + "'"); err == nil {
				t.Fatal("invalid SQL encoding accepted")
			}
			if _, err := c.interpolate(selectBindSQL, []driver.NamedValue{{Value: tc.text}}); !errors.Is(err, charset.ErrEncoding) {
				t.Fatal("invalid parameter encoding accepted", err)
			}
			if _, err := c.encodeSQL("SELECT '\x00'"); !errors.Is(err, ErrBind) {
				t.Fatal("NUL SQL text accepted", err)
			}
		})
	}
}

func TestSQLExecutionEncodedBudget(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, variant := range []string{testStandard, testCompression, testOB20} {
			for _, path := range []string{lobOpenText, "server-explicit", "server-automatic", "client-explicit", "client-automatic"} {
				t.Run(fmt.Sprintf("%d/%s/%s", mode, variant, path), func(t *testing.T) {
					status := byte(2)
					if mode == ModeOracle {
						status = 6
					}
					query := "UPDATE t SET v=? /*" + strings.Repeat("测", 150) + "*/"
					encoded := "UPDATE t SET v=? /*" + strings.Repeat("\xb2\xe2", 150) + "*/"
					cfg := Config{Mode: mode, Session: SessionOptions{Charset: localFilenameCharset}}
					if path == lobOpenText {
						query = strings.Replace(query, "?", "1", 1)
						encoded = strings.Replace(encoded, "?", "1", 1)
					}
					wireQuery := encoded
					if strings.HasPrefix(path, "client") {
						cfg.PrepareMode = PrepareClient
						wireQuery = strings.Replace(encoded, "?", " 1 ", 1)
					}
					cfg.Limits.MaxMessageBytes = int64(len(wireQuery) + 1)
					db := batchRewriteDB(t, cfg, variant, func(ctx context.Context, p *wire.PacketIO) {
						if strings.HasPrefix(path, "server") {
							if !servePrepareStatus(ctx, t, p, encoded, 85, 1, nil, status) {
								return
							}
							payload := append(executeHeader(85), 0, 1, 8, 0, 1, 0, 0, 0, 0, 0, 0, 0)
							if !serveOK(ctx, t, p, payload, status) || !readCommand(ctx, t, p, stmtPacket(25, 85)) {
								return
							}
						} else if !serveOK(ctx, t, p, append([]byte{3}, wireQuery...), status) {
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
					if strings.HasSuffix(path, "explicit") {
						var s *sql.Stmt
						var err error
						s, err = db.PrepareContext(ctx, query)
						if err != nil {
							t.Fatal("fitting SQL rejected during Prepare", err)
						}
						if _, err = s.ExecContext(ctx, int64(1)); err != nil {
							t.Fatal(err)
						}
						if cfg.PrepareMode == PrepareClient {
							if _, err = s.ExecContext(ctx, int64(10)); !errors.Is(err, wire.ErrMessageTooLarge) {
								t.Fatal("oversize client execution accepted", err)
							}
						}
						if err = s.Close(); err != nil {
							t.Fatal(err)
						}
					} else {
						var args []any
						if path != lobOpenText {
							args = []any{int64(1)}
						}
						if _, err := db.ExecContext(ctx, query, args...); err != nil {
							t.Fatal("fitting SQL rejected during execution", err)
						}
						if _, err := db.ExecContext(ctx, query+"xxx", args...); !errors.Is(err, wire.ErrMessageTooLarge) {
							t.Fatal("oversize automatic execution accepted", err)
						}
					}
					if _, err := db.ExecContext(ctx, strings.Replace(query, "?", "1", 1)+"xxx"); !errors.Is(err, wire.ErrMessageTooLarge) {
						t.Fatal("oversize SQL sent", err)
					}
					if err := db.PingContext(ctx); err != nil {
						t.Fatal("SQL budget rejection lost synchronized session", err)
					}
				})
			}
		}
	}
}

func TestOutputSQLTextEncodedBudget(t *testing.T) {
	for _, prepare := range []PrepareMode{PrepareServer, PrepareClient} {
		for _, variant := range []string{testStandard, testCompression, testOB20} {
			t.Run(fmt.Sprintf("%d/%s", prepare, variant), func(t *testing.T) {
				query := "BEGIN p(:v); END; /*" + strings.Repeat("测", 150) + "*/"
				encoded := "BEGIN p(?); END; /*" + strings.Repeat("\xb2\xe2", 150) + "*/"
				payload, err := wire.EncodePrepareExecute(0, []byte(encoded), []wire.Parameter{{Type: 6}}, 4096)
				if err != nil {
					t.Fatal(err)
				}
				cfg := Config{Mode: ModeOracle, PrepareMode: prepare, Session: SessionOptions{Charset: localFilenameCharset}, Limits: Limits{MaxMessageBytes: int64(len(payload))}}
				db := batchRewriteDB(t, cfg, variant, func(ctx context.Context, p *wire.PacketIO) {
					if !readCommand(ctx, t, p, payload) || !sendPackets(ctx, t, p,
						combinedHeader(86, 1, 1, 1), textColumn("p", 8, 63, 0), []byte{254, 0, 0, 6, 0},
						textColumn("v", 8, 63, 0), []byte{254, 0, 0, 6, 16},
						binary.LittleEndian.AppendUint64([]byte{0, 0}, 42), []byte{254, 0, 0, 6, 0}, commandPacket(1, 0, 6)) ||
						!readCommand(ctx, t, p, stmtPacket(25, 86)) || !serveOK(ctx, t, p, []byte{14}, 6) {
						return
					}
					p.ResetSequence()
					serveQuit(t, p)
				})
				defer func() { _ = db.Close() }()
				var value int64
				if _, err := db.ExecContext(context.Background(), query, sql.Named("v", sql.Out{Dest: &value})); err != nil || value != 42 {
					t.Fatal("fitting OUT SQL rejected or output changed", value, err)
				}
				if err := db.PingContext(context.Background()); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
