package oceanbase

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const (
	xaContextCommitSQL  = "XA COMMIT X'67',X'',0x1"
	xaContextBeforeSend = "before-send"
	xaContextAfterSend  = "after-send"
	xaContextDeadline   = "deadline"
)

func TestXACommitContextBoundary(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, framing := range []string{testStandard, testCompression, testOB20} {
			for _, boundary := range []string{xaContextBeforeSend, xaContextAfterSend, xaContextDeadline} {
				t.Run(fmt.Sprintf("%v/%s/%s", mode, framing, boundary), func(t *testing.T) {
					testXACommitContextBoundary(t, mode, framing, boundary)
				})
			}
		}
	}
}

func testXACommitContextBoundary(t *testing.T, mode Mode, framing, boundary string) {
	t.Helper()
	xid := XID{FormatID: 1, GlobalID: []byte("g")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var commands, dials atomic.Int32
	closed := make(chan struct{})
	db := localInfileDB(t, mode, false, func(peerCtx context.Context, p *wire.PacketIO) {
		if boundary == xaContextBeforeSend {
			status := byte(2)
			if mode == ModeOracle {
				status = 6
			}
			if !serveOK(peerCtx, t, p, []byte{14}, status) {
				return
			}
			p.ResetSequence()
			serveQuit(t, p)
			return
		}
		if mode == ModeMySQL {
			if !readQuery(peerCtx, t, p, xaContextCommitSQL) {
				return
			}
		} else {
			payload, err := wire.EncodePrepareExecute(0, []byte("BEGIN ? := SYS.DBMS_XA.XA_COMMIT(?, ?); END;"), []wire.Parameter{{Type: 6}, nativeXID(xid), {Type: 1, Data: []byte{0}}}, 4096)
			if err != nil || !readCommand(peerCtx, t, p, payload) {
				return
			}
		}
		commands.Add(1)
		if boundary == xaContextAfterSend {
			cancel()
		}
		if _, err := p.ReadMessage(peerCtx); !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
			t.Error("unconfirmed XA commit sent another command or failed to close", err)
			return
		}
		close(closed)
	}, func(cfg *Config) {
		switch framing {
		case testCompression:
			cfg.Protocol.Compression = FeatureRequired
		case testOB20:
			cfg.Protocol.OB20 = FeatureRequired
		}
		dial := cfg.DialContext
		cfg.DialContext = func(dialCtx context.Context, network, addr string) (net.Conn, error) {
			dials.Add(1)
			return dial(dialCtx, network, addr)
		}
	})
	defer func() { _ = db.Close() }()
	pinned := pinnedSession(t, db)
	defer func() { _ = pinned.Close() }()
	want := context.Canceled
	switch boundary {
	case xaContextBeforeSend:
		cancel()
	case xaContextDeadline:
		var stop context.CancelFunc
		ctx, stop = context.WithTimeout(ctx, time.Second)
		defer stop()
		want = context.DeadlineExceeded
	}
	err := XACommit(ctx, pinned, xid, false)
	if !errors.Is(err, ErrXA) || !errors.Is(err, want) || errors.Is(err, driver.ErrBadConn) {
		t.Fatal("XA commit lost cancellation evidence", err)
	}
	if boundary == xaContextBeforeSend {
		if errors.Is(err, ErrXAOutcomeUnknown) || errors.Is(err, ErrCommitUnknown) {
			t.Fatal("unsent XA commit was marked unknown", err)
		}
		if err = pinned.PingContext(context.Background()); err != nil {
			t.Fatal("unsent XA commit discarded the original session", err)
		}
	} else {
		if !errors.Is(err, ErrXAOutcomeUnknown) || !errors.Is(err, ErrCommitUnknown) {
			t.Fatal("sent XA commit lost outcome uncertainty", err)
		}
		select {
		case <-closed:
		case <-time.After(3 * time.Second):
			t.Fatal("unconfirmed XA commit left its physical connection open")
		}
		if err = pinned.PingContext(context.Background()); err == nil {
			t.Fatal("unconfirmed XA commit remained usable")
		}
	}
	wantCommands := int32(1)
	if boundary == xaContextBeforeSend {
		wantCommands = 0
	}
	if commands.Load() != wantCommands || dials.Load() != 1 {
		t.Fatalf("unexpected commit replay or redial: commands=%d dials=%d", commands.Load(), dials.Load())
	}
}
