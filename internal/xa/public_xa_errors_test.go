package xa_test

import (
	context "context"
	driver "database/sql/driver"
	binary "encoding/binary"
	errors "errors"
	fmt "fmt"
	testing "testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestXAMySQLErrorCodes(t *testing.T) {
	for _, tc := range []struct {
		server uint16
		state  string
		xa     int32
	}{
		{1397, "XAE04", -4},
		{1398, "XAE05", -5},
		{1399, "XAE07", -7},
		{1400, "XAE09", -9},
		{1401, "XAE03", -3},
		{1402, "XA100", 100},
		{1440, "XAE08", -8},
		{1613, "XA106", 106},
		{1614, "XA102", 102},
		{1064, "42000", -3},
	} {
		for _, framing := range []string{"standard", testCompression, testOB20} {
			for _, commit := range []bool{false, true} {
				t.Run(fmt.Sprintf("%d/%s/commit=%v", tc.server, framing, commit), func(t *testing.T) {
					testXAMySQLErrorCode(t, tc.server, tc.state, tc.xa, framing, commit)
				})
			}
		}
	}
}

func testXAMySQLErrorCode(t *testing.T, code uint16, state string, want int32, framing string, commit bool) {
	t.Helper()
	xid := oceanbase.XID{FormatID: 1, GlobalID: []byte("g")}
	db := localInfileDB(t, config.ModeMySQL, false, func(ctx context.Context, p *wire.PacketIO) {
		serveXAMySQLError(ctx, t, p, code, state, commit)
	}, func(cfg *config.Config) {
		switch framing {
		case testCompression:
			cfg.Protocol.Compression = config.FeatureRequired
		case testOB20:
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
	if commit {
		if err = oceanbase.XAStart(ctx, pinned, xid, oceanbase.XANoFlags); err != nil {
			t.Fatal(err)
		}
		if err = oceanbase.XAEnd(ctx, pinned, xid, oceanbase.XASuccess); err != nil {
			t.Fatal(err)
		}
		err = oceanbase.XACommit(ctx, pinned, xid, true)
	} else {
		err = oceanbase.XAStart(ctx, pinned, xid, oceanbase.XANoFlags)
	}
	assertXAServerRejection(t, err, want, oceanbase.Error{Code: uint32(code), SQLState: state, Message: "XA server rejection"})
	if err = pinned.PingContext(ctx); err != nil {
		t.Fatal("complete rejection discarded session", err)
	}
	if commit {
		if err = oceanbase.XARollback(ctx, pinned, xid); err != nil {
			t.Fatal("server rejection incorrectly completed local branch", err)
		}
	}
	if err = pinned.Close(); err != nil || db.Stats().Idle != 0 {
		t.Fatal("XA connection returned to pool", err)
	}
}

func serveXAMySQLError(ctx context.Context, t *testing.T, p *wire.PacketIO, code uint16, state string, commit bool) {
	t.Helper()
	query := testXAStartSQL
	if commit {
		for _, q := range []string{testXAStartSQL, testXAEndSQL} {
			if !testpeer.ServeOK(ctx, t, p, []byte("\x03"+q), 2) {
				return
			}
		}
		query = "XA COMMIT X'67',X'',0x1 ONE PHASE"
	}
	packet := binary.LittleEndian.AppendUint16([]byte{255}, code)
	packet = append(packet, '#')
	packet = append(packet, state...)
	packet = append(packet, "XA server rejection"...)
	if !testpeer.ReadQuery(ctx, t, p, query) || !testpeer.SendPackets(ctx, t, p, packet) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
		return
	}
	if commit && !testpeer.ServeOK(ctx, t, p, []byte("\x03XA ROLLBACK X'67',X'',0x1"), 2) {
		return
	}
	p.ResetSequence()
	testpeer.ServeQuit(t, p)
}

func assertXAServerRejection(t *testing.T, err error, want int32, cause oceanbase.Error) {
	t.Helper()
	var xa *oceanbase.XAError
	var server *oceanbase.Error
	if !errors.Is(err, oceanbase.ErrXA) || !errors.As(err, &xa) || xa.Code != want || !errors.As(err, &server) || *server != cause {
		t.Errorf("XA classification lost code or cause: got %v, want XA %d / server %+v", err, want, cause)
	}
	if errors.Is(err, oceanbase.ErrXAOutcomeUnknown) || errors.Is(err, oceanbase.ErrCommitUnknown) || errors.Is(err, driver.ErrBadConn) {
		t.Errorf("complete server rejection classified as unknown or replayable: %v", err)
	}
}

func TestXAOracleServerErrorNamespace(t *testing.T) {
	for _, code := range []uint16{1397, 1398, 1399, 1400, 1401, 1402, 1440, 1613, 1614} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			xid := oceanbase.XID{FormatID: 1, GlobalID: []byte("g")}
			db := localInfileDB(t, config.ModeOracle, false, func(ctx context.Context, p *wire.PacketIO) {
				query := "BEGIN ? := SYS.DBMS_XA.XA_ROLLBACK(?); END;"
				want, err := wire.EncodePrepareExecute(0, []byte(query), []wire.Parameter{{Type: 6}, nativeXID(xid)}, 4096)
				if err != nil {
					t.Error(err)
					return
				}
				packet := binary.LittleEndian.AppendUint16([]byte{255}, code)
				packet = append(packet, "#HY000Oracle SQL error"...)
				if !testpeer.ReadCommand(ctx, t, p, want) || !testpeer.SendPackets(ctx, t, p, packet) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 6) {
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
			err = oceanbase.XARollback(ctx, pinned, xid)
			assertXAServerRejection(t, err, -3, oceanbase.Error{Code: uint32(code), SQLState: "HY000", Message: "Oracle SQL error"})
			if err = pinned.PingContext(ctx); err != nil {
				t.Fatal(err)
			}
			if err = pinned.Close(); err != nil || db.Stats().Idle != 0 {
				t.Fatal("rejected XA connection returned to pool", err)
			}
		})
	}
}
