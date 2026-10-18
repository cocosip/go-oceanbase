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

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const outContextQuery = "CALL p(?,?,?)"

func TestOutCancelStopsPublication(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, framing := range []string{testStandard, testCompression, testOB20} {
			t.Run(fmt.Sprintf("%v/%s", mode, framing), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				db := localInfileDB(t, mode, false, func(peerCtx context.Context, p *wire.PacketIO) {
					if !serveOutContextResponse(peerCtx, t, p, mode) {
						return
					}
					status := byte(2)
					if mode == ModeOracle {
						status = 6
					}
					if !serveOK(peerCtx, t, p, []byte{14}, status) {
						return
					}
					p.ResetSequence()
					serveQuit(t, p)
				}, func(cfg *Config) {
					switch framing {
					case testCompression:
						cfg.Protocol.Compression = FeatureRequired
					case testOB20:
						cfg.Protocol.OB20 = FeatureRequired
					}
				})
				defer func() { _ = db.Close() }()
				pinned := pinnedSession(t, db)
				defer func() { _ = pinned.Close() }()
				value, firstCalls, laterCalls := int64(7), 0, 0
				first := &callbackOutScanner{scan: func() { firstCalls++; cancel() }}
				later := &callbackOutScanner{scan: func() { laterCalls++ }}
				_, err := pinned.ExecContext(ctx, outContextQuery, Out{Dest: first, Type: typeNameBigInt}, sql.Out{Dest: &value}, Out{Dest: later, Type: typeNameBigInt})
				if !errors.Is(err, context.Canceled) || firstCalls != 1 || value != 7 || laterCalls != 0 {
					t.Errorf("canceled output publication continued: first=%d value=%d later=%d err=%v", firstCalls, value, laterCalls, err)
				}
				if err = pinned.PingContext(context.Background()); err != nil {
					t.Fatal("completed output response lost its original session", err)
				}
			})
		}
	}
}

func TestOutTransactionCancelStopsPublication(t *testing.T) {
	txCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &conn{cfg: outputTestConfig(), activeTx: &tx{ctx: txCtx}}
	value, laterCalls := int64(7), 0
	args := []any{Out{Dest: &callbackOutScanner{scan: cancel}, Type: typeNameBigInt}, sql.Out{Dest: &value}, Out{Dest: &callbackOutScanner{scan: func() { laterCalls++ }}, Type: typeNameBigInt}}
	bindings := make([]*outBinding, len(args))
	for i, arg := range args {
		binding, err := convertArgument(arg)
		if err != nil {
			t.Fatal(err)
		}
		bindings[i] = binding.(*outBinding)
	}
	err := c.assignOutputsContext(context.Background(), bindings, []driver.Value{int64(42), int64(42), int64(42)})
	if !errors.Is(err, context.Canceled) || value != 7 || laterCalls != 0 {
		t.Fatal("transaction cancellation published later outputs", value, laterCalls, err)
	}
}

func serveOutContextResponse(ctx context.Context, t *testing.T, p *wire.PacketIO, mode Mode) bool {
	t.Helper()
	columns := [][]byte{textColumn("a", 8, 63, 0), textColumn("b", 8, 63, 0), textColumn("c", 8, 63, 0)}
	if mode == ModeOracle {
		row := []byte{0, 0}
		for range columns {
			row = binary.LittleEndian.AppendUint64(row, 42)
		}
		return serveResourceOut(ctx, t, p, outContextQuery, columns, row, false, false) && readCommand(ctx, t, p, stmtPacket(25, 91))
	}
	p.ResetSequence()
	initial, err := p.ReadMessage(ctx)
	pattern := regexp.MustCompile(`^\x03SET (@_obgo_[0-9a-f]{32}_0)=NULL,(@_obgo_[0-9a-f]{32}_1)=NULL,(@_obgo_[0-9a-f]{32}_2)=NULL$`)
	vars := pattern.FindStringSubmatch(string(initial))
	if err != nil || len(vars) != 4 {
		t.Error("unexpected output variable initialization", err)
		return false
	}
	vars = vars[1:]
	if !sendPackets(ctx, t, p, commandPacket(0, 0, 2)) || !readQuery(ctx, t, p, "CALL p( "+strings.Join(vars, " , ")+" )") || !sendPackets(ctx, t, p, commandPacket(0, 0, 2)) || !readQuery(ctx, t, p, "SELECT "+strings.Join(vars, ",")) {
		return false
	}
	if !sendPackets(ctx, t, p, []byte{3}, columns[0], columns[1], columns[2], []byte{254, 0, 0, 2, 0}, []byte{2, '4', '2', 2, '4', '2', 2, '4', '2'}, []byte{254, 0, 0, 2, 0}) || !readQuery(ctx, t, p, "SET "+strings.Join(vars, "=NULL,")+"=NULL") {
		return false
	}
	return sendPackets(ctx, t, p, commandPacket(0, 0, 2))
}
