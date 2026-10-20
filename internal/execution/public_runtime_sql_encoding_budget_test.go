package execution_test

import (
	context "context"
	sql "database/sql"
	driver "database/sql/driver"
	binary "encoding/binary"
	errors "errors"
	fmt "fmt"
	strings "strings"
	testing "testing"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	charset "github.com/cocosip/go-oceanbase/internal/charset"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestOutputSQLTextEncodedBudget(t *testing.T) {
	for _, prepare := range []oceanbase.PrepareMode{oceanbase.PrepareServer, oceanbase.PrepareClient} {
		for _, variant := range []string{testStandard, testCompression, testOB20} {
			t.Run(fmt.Sprintf("%d/%s", prepare, variant), func(t *testing.T) {
				query := "BEGIN p(:v); END; /*" + strings.Repeat("测", 150) + "*/"
				encoded := "BEGIN p(?); END; /*" + strings.Repeat("\xb2\xe2", 150) + "*/"
				payload, err := wire.EncodePrepareExecute(0, []byte(encoded), []wire.Parameter{{Type: 6}}, 4096)
				if err != nil {
					t.Fatal(err)
				}
				cfg := oceanbase.Config{Mode: oceanbase.ModeOracle, PrepareMode: prepare, Session: oceanbase.SessionOptions{Charset: localFilenameCharset}, Limits: oceanbase.Limits{MaxMessageBytes: int64(len(payload))}}
				db := publictest.BatchRewriteDB(t, cfg, variant, func(ctx context.Context, p *wire.PacketIO) {
					if !testpeer.ReadCommand(ctx, t, p, payload) || !testpeer.SendPackets(ctx, t, p,
						publictest.CombinedHeader(86, 1, 1, 1), testpeer.TextColumn("p", 8, 63, 0), []byte{254, 0, 0, 6, 0},
						testpeer.TextColumn("v", 8, 63, 0), []byte{254, 0, 0, 6, 16},
						binary.LittleEndian.AppendUint64([]byte{0, 0}, 42), []byte{254, 0, 0, 6, 0}, publictest.CommandPacket(1, 0, 6)) ||
						!testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 86)) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 6) {
						return
					}
					p.ResetSequence()
					testpeer.ServeQuit(t, p)
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

func TestSQLExecutionEncodedBudget(t *testing.T) {
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, variant := range []string{testStandard, testCompression, testOB20} {
			for _, path := range []string{lobOpenText, "server-explicit", "server-automatic", "client-explicit", "client-automatic"} {
				t.Run(fmt.Sprintf("%d/%s/%s", mode, variant, path), func(t *testing.T) {
					status := byte(2)
					if mode == oceanbase.ModeOracle {
						status = 6
					}
					query := "UPDATE t SET v=? /*" + strings.Repeat("测", 150) + "*/"
					encoded := "UPDATE t SET v=? /*" + strings.Repeat("\xb2\xe2", 150) + "*/"
					cfg := oceanbase.Config{Mode: mode, Session: oceanbase.SessionOptions{Charset: localFilenameCharset}}
					if path == lobOpenText {
						query = strings.Replace(query, "?", "1", 1)
						encoded = strings.Replace(encoded, "?", "1", 1)
					}
					wireQuery := encoded
					if strings.HasPrefix(path, "client") {
						cfg.PrepareMode = oceanbase.PrepareClient
						wireQuery = strings.Replace(encoded, "?", " 1 ", 1)
					}
					cfg.Limits.MaxMessageBytes = int64(len(wireQuery) + 1)
					db := publictest.BatchRewriteDB(t, cfg, variant, func(ctx context.Context, p *wire.PacketIO) {
						if strings.HasPrefix(path, "server") {
							if !publictest.ServePrepareStatus(ctx, t, p, encoded, 85, 1, nil, status) {
								return
							}
							payload := append(publictest.ExecuteHeader(85), 0, 1, 8, 0, 1, 0, 0, 0, 0, 0, 0, 0)
							if !testpeer.ServeOK(ctx, t, p, payload, status) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 85)) {
								return
							}
						} else if !testpeer.ServeOK(ctx, t, p, append([]byte{3}, wireQuery...), status) {
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
						if cfg.PrepareMode == oceanbase.PrepareClient {
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

func TestSQLBudgetStrictEncoding(t *testing.T) {
	for _, tc := range []struct{ name, text string }{
		{localFilenameCharset, "😀"},
		{"utf8mb4", "\xff"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := oceanbase.Config{Session: oceanbase.SessionOptions{Charset: tc.name}, Limits: oceanbase.Limits{MaxMessageBytes: 64}, Mode: oceanbase.ModeOracle}
			op := runtimeOperation(t, cfg)
			if _, err := runtimeEncodeSQL(op, "SELECT '"+tc.text+"'"); err == nil {
				t.Fatal("invalid SQL encoding accepted")
			}
			if _, err := runtimeInterpolate(op, selectBindSQL, []driver.NamedValue{{Value: tc.text}}); !errors.Is(err, charset.ErrEncoding) {
				t.Fatal("invalid parameter encoding accepted", err)
			}
			if _, err := runtimeEncodeSQL(op, "SELECT '\x00'"); !errors.Is(err, oceanbase.ErrBind) {
				t.Fatal("NUL SQL text accepted", err)
			}
		})
	}
}

func TestClientSQLTextEncodedBudget(t *testing.T) {
	for _, tc := range []struct {
		name, charset, query, want string
		mode                       oceanbase.Mode
		args                       []driver.NamedValue
	}{
		{"spans", localFilenameCharset, "SELECT '测测', ?, '测测', ?", "SELECT '\xb2\xe2\xb2\xe2',  1 , '\xb2\xe2\xb2\xe2',  2 ", oceanbase.ModeMySQL, []driver.NamedValue{{Value: int64(1)}, {Value: int64(2)}}},
		{"tail", localFilenameCharset, "SELECT ?, '测测'", "SELECT  1 , '\xb2\xe2\xb2\xe2'", oceanbase.ModeOracle, []driver.NamedValue{{Value: int64(1)}}},
		{"oracle-literal", localFilenameCharset, selectBindSQL, "SELECT  ('" + strings.Repeat(parameterBudgetGBK, 10) + "''\\'||CHR(0)||'') ", oceanbase.ModeOracle, []driver.NamedValue{{Value: strings.Repeat(parameterBudgetText, 10) + "'\\\x00"}}},
		{"mysql-literal", identityCharsetLatin, selectBindSQL, "SELECT  _latin1 X'" + strings.Repeat("80", 30) + "' ", oceanbase.ModeMySQL, []driver.NamedValue{{Value: strings.Repeat("€", 30)}}},
		{"named", localFilenameCharset, "SELECT :v, '测测', :v", "SELECT  '\xb2\xe2\xb2\xe2' , '\xb2\xe2\xb2\xe2',  '\xb2\xe2\xb2\xe2' ", oceanbase.ModeOracle, []driver.NamedValue{{Name: "v", Value: parameterBudgetText}}},
		{"expansion", testAttributeGB18030, selectBindSQL, "SELECT  '" + parameterBudgetExpansion + "' ", oceanbase.ModeOracle, []driver.NamedValue{{Value: "\u0080\u0080"}}},
	} {
		for _, extra := range []int64{1, 0} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, extra), func(t *testing.T) {
				cfg := oceanbase.Config{Session: oceanbase.SessionOptions{Charset: tc.charset}, Limits: oceanbase.Limits{MaxMessageBytes: int64(len(tc.want)) + extra}}
				cfg.Mode = tc.mode
				op := runtimeOperation(t, cfg)
				got, err := runtimeInterpolate(op, tc.query, tc.args)
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

func TestSQLTextEncodedBudget(t *testing.T) {
	for _, tc := range []struct{ name, text, encoded string }{
		{localFilenameCharset, sqlBudgetSelect, "SELECT '\xb2\xe2\xb2\xe2'"},
		{identityCharsetLatin, "SELECT '€€'", "SELECT '\x80\x80'"},
		{testAttributeGB18030, "SELECT '\u0080\u0080'", "SELECT '" + parameterBudgetExpansion + "'"},
		{"utf8mb4", sqlBudgetSelect, sqlBudgetSelect},
	} {
		for _, extra := range []int64{1, 0} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, extra), func(t *testing.T) {
				cfg := oceanbase.Config{Session: oceanbase.SessionOptions{Charset: tc.name}, Limits: oceanbase.Limits{MaxMessageBytes: int64(len(tc.encoded)) + extra}}
				op := runtimeOperation(t, cfg)
				got, err := runtimeEncodeSQL(op, tc.text)
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
