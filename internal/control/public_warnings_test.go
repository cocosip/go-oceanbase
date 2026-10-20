package control_test

import (
	context "context"
	errors "errors"
	reflect "reflect"
	testing "testing"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	dberrors "github.com/cocosip/go-oceanbase/dberrors"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestWarningDetailsReenable(t *testing.T) {
	for _, path := range []string{"prepared-server", warningClientPreparePath, serverErrorPath} {
		t.Run(path, func(t *testing.T) {
			db := localInfileDB(t, config.ModeMySQL, false, func(ctx context.Context, p *wire.PacketIO) {
				serveWarningReenable(ctx, t, p, path)
			}, func(cfg *config.Config) {
				if path == warningClientPreparePath {
					cfg.PrepareMode = config.PrepareClient
				}
			})
			defer func() { _ = db.Close() }()
			ctx := context.Background()
			pinned, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = pinned.Close() }()
			if err = oceanbase.ClearWarnings(ctx, pinned); err != nil {
				t.Fatal(err)
			}
			if path == serverErrorPath {
				_, err = pinned.ExecContext(ctx, warningSQL)
				var server *oceanbase.Error
				if !errors.As(err, &server) {
					t.Fatal(err)
				}
			} else if _, err = pinned.ExecContext(ctx, testBatchSQL, 1); err != nil {
				t.Fatal(err)
			}
			if got, err := oceanbase.ReadWarnings(ctx, pinned); err != nil || len(got) != 1 || got[0].Code != 1265 {
				t.Fatalf("execution did not reenable warnings=%+v %v", got, err)
			}
		})
	}
}

func TestWarningDetailsPoolReset(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		for range 2 {
			if !testpeer.ReadQuery(ctx, t, p, warningSQL) || !testpeer.SendPackets(ctx, t, p, []byte{0, 1, 0, 2, 0, 1, 0}) {
				return
			}
		}
		if !serveWarningRows(ctx, t, p, []byte("\x07Warning\x041265\x04text")) {
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
	if got, err := oceanbase.ReadWarnings(ctx, pinned); err != nil || len(got) != 0 {
		t.Fatalf("pool reset exposed previous borrower's details=%+v %v", got, err)
	}
	if _, err = pinned.ExecContext(ctx, warningSQL); err != nil {
		t.Fatal(err)
	}
	if got, err := oceanbase.ReadWarnings(ctx, pinned); err != nil || len(got) != 1 {
		t.Fatalf("new SQL after reset did not enable details=%+v %v", got, err)
	}
}

func TestWarningDetailsRowError(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeOK(ctx, t, p, []byte("\x03"+warningSQL), 2) || !testpeer.ReadQuery(ctx, t, p, "SHOW WARNINGS") {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{3}, testpeer.TextColumn("Level", 253, 45, 0), testpeer.TextColumn("Code", 3, 63, 32), testpeer.TextColumn("Message", 253, 45, 0), []byte{254, 0, 0, 2, 0}, []byte("\x07Warning\x041265\x04text"), []byte{255, 21, 4, '#', '4', '2', '0', '0', '0', 'x'}) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
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
	if _, err = pinned.ExecContext(ctx, warningSQL); err != nil {
		t.Fatal(err)
	}
	got, err := oceanbase.ReadWarnings(ctx, pinned)
	var server *oceanbase.Error
	if !errors.As(err, &server) || server.Code != 1045 || got != nil {
		t.Fatalf("failed result returned partial warnings=%+v %v", got, err)
	}
	if err = pinned.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestWarningDetailsTransaction(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}}) || !testpeer.ReadQuery(ctx, t, p, warningSQL) || !testpeer.SendPackets(ctx, t, p, []byte{0, 1, 0, 3, 0, 1, 0}) || !testpeer.ReadQuery(ctx, t, p, "SHOW WARNINGS") {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{3}, testpeer.TextColumn("Level", 253, 45, 0), testpeer.TextColumn("Code", 3, 63, 32), testpeer.TextColumn("Message", 253, 45, 0), []byte{254, 0, 0, 3, 0}, []byte("\x07Warning\x041265\x04text"), []byte{254, 0, 0, 3, 0}) || !serveTxSteps(ctx, t, p, []txStep{{testRollbackSQL, 2}}) {
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
	tx, err := pinned.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, warningSQL); err != nil {
		t.Fatal(err)
	}
	if got, err := oceanbase.ReadWarnings(ctx, pinned); err != nil || len(got) != 1 || got[0].Code != 1265 {
		t.Fatalf("transaction warning=%+v %v", got, err)
	}
	if err = oceanbase.ClearWarnings(ctx, pinned); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestWarningDetailsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db := queryDB(t, func(peerCtx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeOK(peerCtx, t, p, []byte("\x03"+warningSQL), 2) || !testpeer.ReadQuery(peerCtx, t, p, "SHOW WARNINGS") {
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
	if got, err := oceanbase.ReadWarnings(ctx, pinned); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("canceled warnings=%+v %v", got, err)
	}
	if err = pinned.PingContext(context.Background()); err == nil {
		t.Fatal("canceled warning retrieval left session usable")
	}
}

func TestWarningDetailsFraming(t *testing.T) {
	for _, framing := range []string{testCompression, testOB20} {
		t.Run(framing, func(t *testing.T) {
			db := localInfileDB(t, config.ModeMySQL, false, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ServeOK(ctx, t, p, []byte("\x03"+warningSQL), 2) || !testpeer.ReadQuery(ctx, t, p, "SHOW WARNINGS") || !p.Framed() {
					return
				}
				if !testpeer.SendPackets(ctx, t, p, []byte{3}, testpeer.TextColumn("Level", 253, 45, 0), testpeer.TextColumn("Code", 3, 63, 32), testpeer.TextColumn("Message", 253, 28, 0), []byte{254, 0, 0, 2, 0}, []byte("\x07Warning\x041265\x04\xd6\xd0\xce\xc4"), []byte{254, 0, 0, 2, 0}) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			}, func(cfg *config.Config) {
				if framing == testCompression {
					cfg.Protocol.Compression = config.FeatureRequired
				} else {
					cfg.Protocol.OB20 = config.FeatureRequired
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
			got, err := oceanbase.ReadWarnings(ctx, pinned)
			if err != nil || len(got) != 1 || got[0] != (oceanbase.Warning{Level: "Warning", Code: 1265, Message: "\u4e2d\u6587"}) {
				t.Fatalf("framed GBK warning=%+v %v", got, err)
			}
		})
	}
}

func TestWarningDetailsValidation(t *testing.T) {
	if _, err := oceanbase.ReadWarnings(context.Background(), nil); err == nil {
		t.Fatal("nil connection accepted")
	}
	if err := oceanbase.ClearWarnings(context.Background(), nil); err == nil {
		t.Fatal("nil clear accepted")
	}
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
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = oceanbase.ReadWarnings(canceled, pinned); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err = oceanbase.ClearWarnings(canceled, pinned); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err = pinned.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestWarningDetailsResponse(t *testing.T) {
	for _, path := range []string{"null-field", "code-overflow", "extra-warning-result", warningLeadingPath, warningColumnsPath, "ok", warningRowLimitPath, warningByteLimitPath, serverErrorPath} {
		t.Run(path, func(t *testing.T) { testWarningDetailsResponse(t, path) })
	}
}

func TestWarningDetailsClear(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		// A zero protocol count does not prove the server diagnostic area is empty.
		if !testpeer.ServeOK(ctx, t, p, []byte("\x03"+warningSQL), 2) || !serveWarningRows(ctx, t, p) ||
			!sessionScalar(ctx, t, p, "SELECT DATABASE()", 2, "app") || !serveWarningRows(ctx, t, p, []byte("\x05Error\x041005\x04live")) {
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
	if _, err = oceanbase.ReadWarnings(ctx, pinned); err != nil {
		t.Fatal(err)
	}
	if err = oceanbase.ClearWarnings(ctx, pinned); err != nil {
		t.Fatal(err)
	}
	if _, err = pinned.ExecContext(ctx, warningSQL); err != nil {
		t.Fatal(err)
	}
	if got, err := oceanbase.ReadWarnings(ctx, pinned); err != nil || len(got) != 0 {
		t.Fatalf("empty server warnings=%+v %v", got, err)
	}
	if _, err = oceanbase.CurrentSchema(ctx, pinned); err != nil {
		t.Fatal(err)
	}
	got, err := oceanbase.ReadWarnings(ctx, pinned)
	if err != nil || len(got) != 1 || got[0].Message != "live" {
		t.Fatalf("current diagnostic area=%+v %v", got, err)
	}
	if got, err := oceanbase.Warnings(ctx, pinned); err != nil || got != (oceanbase.WarningSummary{Known: true}) {
		t.Fatalf("live details changed ordinary SQL snapshot=%+v %v", got, err)
	}
}

func TestWarningDetailsModes(t *testing.T) {
	db := txDB(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadQuery(ctx, t, p, warningSQL) || !testpeer.SendPackets(ctx, t, p, []byte{0, 1, 0, 6, 0, 1, 0}) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 6) {
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
	if _, err = pinned.ExecContext(ctx, warningSQL); err != nil {
		t.Fatal(err)
	}
	if _, err = oceanbase.ReadWarnings(ctx, pinned); !errors.Is(err, dberrors.ErrUnsupported) {
		t.Fatalf("Oracle warning details=%v", err)
	}
	if got, err := oceanbase.Warnings(ctx, pinned); err != nil || got != (oceanbase.WarningSummary{Count: 1, Known: true}) {
		t.Fatalf("Oracle summary=%+v %v", got, err)
	}
	if err = oceanbase.ClearWarnings(ctx, pinned); err != nil {
		t.Fatal(err)
	}
	if err = pinned.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestWarningDetails(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadQuery(ctx, t, p, warningSQL) || !testpeer.SendPackets(ctx, t, p, []byte{0, 1, 0, 2, 0, 2, 0}) {
			return
		}
		for range 2 {
			if !serveWarningRows(ctx, t, p, []byte("\x07Warning\x041265\x09truncated"), []byte("\x04Note\x041005\x04note")) {
				return
			}
		}
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
	if _, err = pinned.ExecContext(ctx, warningSQL); err != nil {
		t.Fatal(err)
	}
	want := []oceanbase.Warning{{Level: "Warning", Code: 1265, Message: "truncated"}, {Level: "Note", Code: 1005, Message: "note"}}
	for range 2 {
		got, err := oceanbase.ReadWarnings(ctx, pinned)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("warnings=%+v error=%v", got, err)
		}
		got[0].Message = "caller-owned"
	}
	if got, err := oceanbase.Warnings(ctx, pinned); err != nil || got != (oceanbase.WarningSummary{Count: 2, Known: true}) {
		t.Fatalf("detail query overwrote summary: %+v %v", got, err)
	}
	if err = oceanbase.ClearWarnings(ctx, pinned); err != nil {
		t.Fatal(err)
	}
	if got, err := oceanbase.Warnings(ctx, pinned); err != nil || got != (oceanbase.WarningSummary{Known: true}) {
		t.Fatalf("clear summary=%+v %v", got, err)
	}
	if got, err := oceanbase.ReadWarnings(ctx, pinned); err != nil || len(got) != 0 {
		t.Fatalf("cleared warnings=%+v %v", got, err)
	}
	if err = pinned.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
}
