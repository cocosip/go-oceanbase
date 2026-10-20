package statement

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestPrepareServerAuthoritativeSlots(t *testing.T) {
	query := "SELECT '?'"
	ctx, op := protocolOperation(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadCommand(ctx, t, p, append([]byte{22}, query...)) {
			return
		}
		header := []byte{0, 7, 0, 0, 0, 0, 0, 2, 0, 0, 0, 0}
		if testpeer.SendPackets(ctx, t, p, header, testpeer.TextColumn("p", 253, 45, 0), testpeer.TextColumn("p", 253, 45, 0), []byte{254, 0, 0, 2, 0}) {
			testpeer.ReadCommand(ctx, t, p, binary.LittleEndian.AppendUint32([]byte{25}, 7))
		}
	})
	s, err := Prepare(ctx, op, query)
	if err != nil || s.ID() != 7 || s.Plan().NumInput != 2 || len(s.Plan().Slots) != 2 || len(s.Parameters()) != 2 {
		t.Fatal(s, err)
	}
	plan := s.Plan()
	plan.Slots[0].Index = 99
	if s.Plan().Slots[0].Index != 0 {
		t.Fatal("caller mutated owned plan")
	}
	use, err := s.AcquireLease(op)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(ctx, op); err != nil || s.closed {
		t.Fatal("close bypassed lease", err)
	}
	if err = use.Release(ctx, op); err != nil || !s.closed {
		t.Fatal(err)
	}
}

func TestPrepareOracleCountMismatch(t *testing.T) {
	ctx, op := protocolOperation(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if testpeer.ReadCommand(ctx, t, p, []byte("\x16SELECT ? FROM dual")) {
			testpeer.SendPackets(ctx, t, p, []byte{0, 7, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
		}
	})
	if _, err := Prepare(ctx, op, "SELECT :x FROM dual"); !errors.Is(err, dberrors.ErrBind) || !op.Closed() || op.ResponseActive() {
		t.Fatal("server slot mismatch accepted", err)
	}
}
