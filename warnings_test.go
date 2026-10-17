package oceanbase

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const warningRowLimitPath = "row-limit"

const warningColumnsPath = "warning-columns"

const warningClientPreparePath = "prepared-client"

const warningSQL = "UPDATE t SET v=1"
const warningByteLimitPath = "byte-limit"
const warningLeadingPath = "leading-result"

func serveWarningRows(ctx context.Context, t *testing.T, p *wire.PacketIO, rows ...[]byte) bool {
	t.Helper()
	if !readQuery(ctx, t, p, "SHOW WARNINGS") || !sendPackets(ctx, t, p, []byte{3}, textColumn("Level", 253, 45, 0), textColumn("Code", 3, 63, 32), textColumn("Message", 253, 45, 0), []byte{254, 0, 0, 2, 0}) {
		return false
	}
	for _, row := range rows {
		if !sendPackets(ctx, t, p, row) {
			return false
		}
	}
	return sendPackets(ctx, t, p, []byte{254, 0, 0, 2, 0})
}

func TestWarningDetails(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !readQuery(ctx, t, p, warningSQL) || !sendPackets(ctx, t, p, []byte{0, 1, 0, 2, 0, 2, 0}) {
			return
		}
		for range 2 {
			if !serveWarningRows(ctx, t, p, []byte("\x07Warning\x041265\x09truncated"), []byte("\x04Note\x041005\x04note")) {
				return
			}
		}
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
	if _, err = pinned.ExecContext(ctx, warningSQL); err != nil {
		t.Fatal(err)
	}
	want := []Warning{{Level: "Warning", Code: 1265, Message: "truncated"}, {Level: "Note", Code: 1005, Message: "note"}}
	for range 2 {
		got, err := ReadWarnings(ctx, pinned)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("warnings=%+v error=%v", got, err)
		}
		got[0].Message = "caller-owned"
	}
	if got, err := Warnings(ctx, pinned); err != nil || got != (WarningSummary{Count: 2, Known: true}) {
		t.Fatalf("detail query overwrote summary: %+v %v", got, err)
	}
	if err = ClearWarnings(ctx, pinned); err != nil {
		t.Fatal(err)
	}
	if got, err := Warnings(ctx, pinned); err != nil || got != (WarningSummary{Known: true}) {
		t.Fatalf("clear summary=%+v %v", got, err)
	}
	if got, err := ReadWarnings(ctx, pinned); err != nil || len(got) != 0 {
		t.Fatalf("cleared warnings=%+v %v", got, err)
	}
	if err = pinned.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestWarningDetailsModes(t *testing.T) {
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !readQuery(ctx, t, p, warningSQL) || !sendPackets(ctx, t, p, []byte{0, 1, 0, 6, 0, 1, 0}) || !serveOK(ctx, t, p, []byte{14}, 6) {
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
	if _, err = pinned.ExecContext(ctx, warningSQL); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadWarnings(ctx, pinned); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Oracle warning details=%v", err)
	}
	if got, err := Warnings(ctx, pinned); err != nil || got != (WarningSummary{Count: 1, Known: true}) {
		t.Fatalf("Oracle summary=%+v %v", got, err)
	}
	if err = ClearWarnings(ctx, pinned); err != nil {
		t.Fatal(err)
	}
	if err = pinned.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestWarningDetailsClear(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		// A zero protocol count does not prove the server diagnostic area is empty.
		if !serveOK(ctx, t, p, []byte("\x03"+warningSQL), 2) || !serveWarningRows(ctx, t, p) ||
			!sessionScalar(ctx, t, p, "SELECT DATABASE()", 2, "app") || !serveWarningRows(ctx, t, p, []byte("\x05Error\x041005\x04live")) {
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
	if _, err = ReadWarnings(ctx, pinned); err != nil {
		t.Fatal(err)
	}
	if err = ClearWarnings(ctx, pinned); err != nil {
		t.Fatal(err)
	}
	if _, err = pinned.ExecContext(ctx, warningSQL); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadWarnings(ctx, pinned); err != nil || len(got) != 0 {
		t.Fatalf("empty server warnings=%+v %v", got, err)
	}
	if _, err = CurrentSchema(ctx, pinned); err != nil {
		t.Fatal(err)
	}
	got, err := ReadWarnings(ctx, pinned)
	if err != nil || len(got) != 1 || got[0].Message != "live" {
		t.Fatalf("current diagnostic area=%+v %v", got, err)
	}
	if got, err := Warnings(ctx, pinned); err != nil || got != (WarningSummary{Known: true}) {
		t.Fatalf("live details changed ordinary SQL snapshot=%+v %v", got, err)
	}
}

func TestWarningDetailsResponse(t *testing.T) {
	for _, path := range []string{"null-field", "code-overflow", "extra-warning-result", warningLeadingPath, warningColumnsPath, "ok", warningRowLimitPath, warningByteLimitPath, serverErrorPath} {
		t.Run(path, func(t *testing.T) { testWarningDetailsResponse(t, path) })
	}
}

func testWarningDetailsResponse(t *testing.T, path string) {
	t.Helper()
	db := localInfileDB(t, ModeMySQL, false, func(ctx context.Context, p *wire.PacketIO) {
		serveWarningDetailsResponse(ctx, t, p, path)
	}, func(cfg *Config) {
		cfg.Limits.MaxMessageBytes = 128
		if path == warningRowLimitPath {
			cfg.Limits.MaxResultItems = 1
		}
	})
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	if _, err = pinned.ExecContext(ctx, warningSQL); err != nil {
		t.Fatal(err)
	}
	got, err := ReadWarnings(ctx, pinned)
	if err == nil || got != nil {
		t.Fatalf("invalid warnings returned partial data=%+v %v", got, err)
	}
	if path == serverErrorPath {
		var server *Error
		if !errors.As(err, &server) || server.Code != 1045 {
			t.Fatal(err)
		}
		if err = pinned.PingContext(ctx); err != nil {
			t.Fatal(err)
		}
	} else {
		if err = pinned.Raw(func(raw any) error {
			if !raw.(*conn).closed.Load() {
				return errors.New("invalid warning response did not discard session")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err = pinned.PingContext(ctx); err == nil {
			t.Fatal("invalid warning response remained usable")
		}
	}
}

func serveWarningDetailsResponse(ctx context.Context, t *testing.T, p *wire.PacketIO, path string) {
	t.Helper()
	if !serveOK(ctx, t, p, []byte("\x03"+warningSQL), 2) || !readQuery(ctx, t, p, "SHOW WARNINGS") {
		return
	}
	if path == "ok" {
		if !sendPackets(ctx, t, p, commandPacket(0, 0, 2)) {
			return
		}
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("non-rowset warning response was reused")
		}
		return
	}
	if path == serverErrorPath {
		if !sendPackets(ctx, t, p, []byte{255, 21, 4, '#', '4', '2', '0', '0', '0', 'x'}) || !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
		return
	}
	if path == warningLeadingPath && !sendPackets(ctx, t, p, commandPacket(0, 0, 10)) {
		return
	}
	columns := byte(3)
	if path == warningColumnsPath {
		columns = 2
	}
	if !sendPackets(ctx, t, p, []byte{columns}) {
		return
	}
	for range columns {
		if !sendPackets(ctx, t, p, textColumn("v", 253, 45, 0)) {
			return
		}
	}
	if !sendPackets(ctx, t, p, []byte{254, 0, 0, 2, 0}) {
		return
	}
	if path == warningLeadingPath {
		// A correct client can close immediately after metadata; both writes may then fail.
		_ = p.WriteMessage(ctx, []byte("\x07Warning\x041265\x04text"))
		_ = p.WriteMessage(ctx, []byte{254, 0, 0, 2, 0})
		_, _ = p.ReadMessage(ctx)
		return
	}
	row := []byte("\x07Warning\x041265\x04text")
	switch path {
	case "null-field":
		row = []byte("\xfb\x041265\x04text")
	case "code-overflow":
		row = []byte("\x07Warning\x0a4294967296\x04text")
	case warningByteLimitPath:
		row = append([]byte("\x07Warning\x041265\x5a"), strings.Repeat("x", 90)...)
	}
	if path != warningColumnsPath && !sendPackets(ctx, t, p, row) {
		return
	}
	if (path == warningRowLimitPath || path == warningByteLimitPath) && !sendPackets(ctx, t, p, row) {
		return
	}
	if path == "extra-warning-result" && !sendPackets(ctx, t, p, []byte{254, 0, 0, 10, 0}) {
		return
	}
	if _, err := p.ReadMessage(ctx); err == nil {
		t.Error("invalid warning response was reused")
	}
}

func TestWarningDetailsValidation(t *testing.T) {
	if _, err := ReadWarnings(context.Background(), nil); err == nil {
		t.Fatal("nil connection accepted")
	}
	if err := ClearWarnings(context.Background(), nil); err == nil {
		t.Fatal("nil clear accepted")
	}
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
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = ReadWarnings(canceled, pinned); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err = ClearWarnings(canceled, pinned); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err = pinned.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestWarningDetailsFraming(t *testing.T) {
	for _, framing := range []string{testCompression, testOB20} {
		t.Run(framing, func(t *testing.T) {
			db := localInfileDB(t, ModeMySQL, false, func(ctx context.Context, p *wire.PacketIO) {
				if !serveOK(ctx, t, p, []byte("\x03"+warningSQL), 2) || !readQuery(ctx, t, p, "SHOW WARNINGS") || !p.Framed() {
					return
				}
				if !sendPackets(ctx, t, p, []byte{3}, textColumn("Level", 253, 45, 0), textColumn("Code", 3, 63, 32), textColumn("Message", 253, 28, 0), []byte{254, 0, 0, 2, 0}, []byte("\x07Warning\x041265\x04\xd6\xd0\xce\xc4"), []byte{254, 0, 0, 2, 0}) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			}, func(cfg *Config) {
				if framing == testCompression {
					cfg.Protocol.Compression = FeatureRequired
				} else {
					cfg.Protocol.OB20 = FeatureRequired
				}
			})
			defer func() { _ = db.Close() }()
			ctx := context.Background()
			pinned, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = pinned.Close() }()
			if _, err = pinned.ExecContext(ctx, warningSQL); err != nil {
				t.Fatal(err)
			}
			got, err := ReadWarnings(ctx, pinned)
			if err != nil || len(got) != 1 || got[0] != (Warning{Level: "Warning", Code: 1265, Message: "\u4e2d\u6587"}) {
				t.Fatalf("framed GBK warning=%+v %v", got, err)
			}
		})
	}
}

func TestWarningDetailsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db := queryDB(t, func(peerCtx context.Context, p *wire.PacketIO) {
		if !serveOK(peerCtx, t, p, []byte("\x03"+warningSQL), 2) || !readQuery(peerCtx, t, p, "SHOW WARNINGS") {
			return
		}
		cancel()
		if _, err := p.ReadMessage(peerCtx); err == nil {
			t.Error("canceled warning retrieval left transport open")
		}
	})
	defer func() { _ = db.Close() }()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	if _, err = pinned.ExecContext(ctx, warningSQL); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadWarnings(ctx, pinned); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("canceled warnings=%+v %v", got, err)
	}
	if err = pinned.PingContext(context.Background()); err == nil {
		t.Fatal("canceled warning retrieval left session usable")
	}
}

func TestWarningDetailsTransaction(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}}) || !readQuery(ctx, t, p, warningSQL) || !sendPackets(ctx, t, p, []byte{0, 1, 0, 3, 0, 1, 0}) || !readQuery(ctx, t, p, "SHOW WARNINGS") {
			return
		}
		if !sendPackets(ctx, t, p, []byte{3}, textColumn("Level", 253, 45, 0), textColumn("Code", 3, 63, 32), textColumn("Message", 253, 45, 0), []byte{254, 0, 0, 3, 0}, []byte("\x07Warning\x041265\x04text"), []byte{254, 0, 0, 3, 0}) || !serveTxSteps(ctx, t, p, []txStep{{testRollbackSQL, 2}}) {
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
	tx, err := pinned.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, warningSQL); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadWarnings(ctx, pinned); err != nil || len(got) != 1 || got[0].Code != 1265 {
		t.Fatalf("transaction warning=%+v %v", got, err)
	}
	if err = ClearWarnings(ctx, pinned); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestWarningDetailsRowError(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveOK(ctx, t, p, []byte("\x03"+warningSQL), 2) || !readQuery(ctx, t, p, "SHOW WARNINGS") {
			return
		}
		if !sendPackets(ctx, t, p, []byte{3}, textColumn("Level", 253, 45, 0), textColumn("Code", 3, 63, 32), textColumn("Message", 253, 45, 0), []byte{254, 0, 0, 2, 0}, []byte("\x07Warning\x041265\x04text"), []byte{255, 21, 4, '#', '4', '2', '0', '0', '0', 'x'}) || !serveOK(ctx, t, p, []byte{14}, 2) {
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
	if _, err = pinned.ExecContext(ctx, warningSQL); err != nil {
		t.Fatal(err)
	}
	got, err := ReadWarnings(ctx, pinned)
	var server *Error
	if !errors.As(err, &server) || server.Code != 1045 || got != nil {
		t.Fatalf("failed result returned partial warnings=%+v %v", got, err)
	}
	if err = pinned.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestWarningDetailsPoolReset(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		for range 2 {
			if !readQuery(ctx, t, p, warningSQL) || !sendPackets(ctx, t, p, []byte{0, 1, 0, 2, 0, 1, 0}) {
				return
			}
		}
		if !serveWarningRows(ctx, t, p, []byte("\x07Warning\x041265\x04text")) {
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
	if _, err = pinned.ExecContext(ctx, warningSQL); err != nil {
		t.Fatal(err)
	}
	if err = pinned.Close(); err != nil {
		t.Fatal(err)
	}
	pinned, err = db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	if got, err := ReadWarnings(ctx, pinned); err != nil || len(got) != 0 {
		t.Fatalf("pool reset exposed previous borrower's details=%+v %v", got, err)
	}
	if _, err = pinned.ExecContext(ctx, warningSQL); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadWarnings(ctx, pinned); err != nil || len(got) != 1 {
		t.Fatalf("new SQL after reset did not enable details=%+v %v", got, err)
	}
}

func TestWarningDetailsReenable(t *testing.T) {
	for _, path := range []string{"prepared-server", warningClientPreparePath, serverErrorPath} {
		t.Run(path, func(t *testing.T) {
			db := localInfileDB(t, ModeMySQL, false, func(ctx context.Context, p *wire.PacketIO) {
				serveWarningReenable(ctx, t, p, path)
			}, func(cfg *Config) {
				if path == warningClientPreparePath {
					cfg.PrepareMode = PrepareClient
				}
			})
			defer func() { _ = db.Close() }()
			ctx := context.Background()
			pinned, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = pinned.Close() }()
			if err = ClearWarnings(ctx, pinned); err != nil {
				t.Fatal(err)
			}
			if path == serverErrorPath {
				_, err = pinned.ExecContext(ctx, warningSQL)
				var server *Error
				if !errors.As(err, &server) {
					t.Fatal(err)
				}
			} else if _, err = pinned.ExecContext(ctx, testBatchSQL, 1); err != nil {
				t.Fatal(err)
			}
			if got, err := ReadWarnings(ctx, pinned); err != nil || len(got) != 1 || got[0].Code != 1265 {
				t.Fatalf("execution did not reenable warnings=%+v %v", got, err)
			}
		})
	}
}

func serveWarningReenable(ctx context.Context, t *testing.T, p *wire.PacketIO, path string) {
	t.Helper()
	switch path {
	case "prepared-server":
		if !servePrepare(ctx, t, p, testBatchSQL, 7, 1, nil) || !readCommand(ctx, t, p, batchExecutePacket(t, 1)) || !sendPackets(ctx, t, p, []byte{0, 1, 0, 2, 0, 1, 0}) || !readCommand(ctx, t, p, stmtPacket(25, 7)) {
			return
		}
	case warningClientPreparePath:
		if !readQuery(ctx, t, p, "INSERT INTO t VALUES ( 1 )") || !sendPackets(ctx, t, p, []byte{0, 1, 0, 2, 0, 1, 0}) {
			return
		}
	case serverErrorPath:
		if !readQuery(ctx, t, p, warningSQL) || !sendPackets(ctx, t, p, []byte{255, 21, 4, '#', '4', '2', '0', '0', '0', 'x'}) {
			return
		}
	}
	if !serveWarningRows(ctx, t, p, []byte("\x07Warning\x041265\x04text")) {
		return
	}
	p.ResetSequence()
	serveQuit(t, p)
}
