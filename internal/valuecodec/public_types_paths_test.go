package valuecodec_test

import (
	"context"
	"database/sql"
	"errors"
	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"reflect"
	"testing"
	"time"
)

const testClientStrategy = "client"
const testServerStrategy = "server"
const testTextStrategy = "text"
const testCharsetUTF16 = "utf16"

func TestScalarSQLCharset(t *testing.T) {
	for _, path := range []string{testTextStrategy, testServerStrategy, testClientStrategy} {
		t.Run(path, func(t *testing.T) {
			cfg := oceanbase.Config{User: publictest.TestUser, Session: oceanbase.SessionOptions{Charset: "gbk"}}
			if path == testClientStrategy {
				cfg.PrepareMode = oceanbase.PrepareClient
			}
			cfg.DialContext = testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeOK(ctx, t, p, []byte("\x03SET NAMES gbk"), 2) || !testpeer.ServeOK(ctx, t, p, []byte("\x03SET autocommit=1"), 2) || !testpeer.ServeVersion(ctx, t, p, 2, false) || !testpeer.ServeOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+00:00'"), 2) {
					return
				}
				ctx = context.Background()
				query := "SELECT '\xd6\xd0'"
				row := []byte{2, 0xd6, 0xd0}
				if path == testServerStrategy {
					if !publictest.ServePrepare(ctx, t, p, query+",?", 33, 1, nil) || !testpeer.ReadCommand(ctx, t, p, append(publictest.ExecuteHeader(33), 0, 1, 253, 0, 2, 0xd6, 0xd0)) {
						return
					}
					row = append([]byte{0, 0}, row...)
				} else {
					if path == testClientStrategy {
						query += ", _gbk X'd6d0' "
					}
					if !testpeer.ReadQuery(ctx, t, p, query) {
						return
					}
				}
				if !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("\xd6\xd0", 253, 28, 0), []byte{254, 0, 0, 2, 0}, row, []byte{254, 0, 0, 2, 0}) {
					return
				}
				if path == testServerStrategy && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 33)) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			connector, err := oceanbase.NewConnector(cfg)
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()
			query := "SELECT '中'"
			var args []any
			if path != testTextStrategy {
				query += ",?"
				args = []any{"中"}
			}
			rows, err := db.QueryContext(context.Background(), query, args...)
			if err != nil {
				t.Fatal(err)
			}
			names, err := rows.Columns()
			if err != nil || len(names) != 1 || names[0] != "中" {
				t.Fatal("metadata encoding", names, err)
			}
			var value string
			if !rows.Next() || rows.Scan(&value) != nil || value != "中" {
				t.Fatal("value encoding", value, rows.Err())
			}
			if err = rows.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOracleParseTime(t *testing.T) {
	options := oceanbase.SessionOptions{ParseTime: true, Location: time.UTC}
	data := []byte{20, 24, 2, 29, 12, 34, 56, 21, 205, 91, 7, 9}
	want := time.Date(2024, 2, 29, 12, 34, 56, 123456789, time.UTC)
	for _, binaryRows := range []bool{false, true} {
		value, err := valuecodec.DecodeRowValue(wire.Column{Type: 202}, binaryRows, data, oceanbase.ModeOracle, options)
		if err != nil || value != want {
			t.Fatal("Oracle TIMESTAMP ignored parseTime", value, err)
		}
	}
	if valuecodec.ColumnScanType(wire.Column{Type: 202}, oceanbase.ModeOracle, options) != reflect.TypeFor[time.Time]() || valuecodec.ColumnScanType(wire.Column{Type: 201}, oceanbase.ModeOracle, options) != reflect.TypeFor[time.Time]() {
		t.Fatal("parseTime scan metadata disagrees with values")
	}
}

func TestScalarSQLRejectsInvalidBeforeSend(t *testing.T) {
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	for _, query := range []string{"SELECT '\x00'", "SELECT '\xff'"} {
		if _, err := db.ExecContext(context.Background(), query); err == nil {
			t.Fatal("invalid SQL sent", query)
		}
	}
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestScalarClientCharsetRestrictions(t *testing.T) {
	if _, ok := charset.HandshakeID(testCharsetUTF16); ok {
		t.Fatal("UTF16 cannot be used as a SQL client charset")
	}
	if _, err := oceanbase.NewConnector(oceanbase.Config{User: publictest.TestUser, Session: oceanbase.SessionOptions{NCharset: "gb18030_2022"}}); err != nil {
		t.Fatal("GB18030-2022 national charset rejected", err)
	}
}

func TestScalarBinaryMaterializationLimit(t *testing.T) {
	cfg := oceanbase.Config{User: publictest.TestUser, Mode: oceanbase.ModeOracle, Limits: oceanbase.Limits{MaxLOBBytes: 2}}
	cfg.DialContext = testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeConnect(ctx, t, p, 6) || !testpeer.ServeBaseline(ctx, t, p, 6) {
			return
		}
		ctx = context.Background()
		if !publictest.ServePrepare(ctx, t, p, "INSERT INTO t VALUES (?)", 44, 1, nil) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 44)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	stmt, err := db.PrepareContext(context.Background(), "INSERT INTO t VALUES (?)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stmt.Close() }()
	for _, value := range []any{oceanbase.Raw{1, 2, 3}, []byte{1, 2, 3}} {
		if _, err = stmt.ExecContext(context.Background(), value); !errors.Is(err, wire.ErrMessageTooLarge) {
			t.Fatal("binary argument escaped MaxLOBBytes", err)
		}
	}
}
