package statement_test

import (
	"context"
	"database/sql/driver"
	"sync/atomic"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/parameters"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/streaminput"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func statementOperation(t testing.TB, cfg config.Config, p *session.Negotiated) (*session.Session, *session.Operation) {
	t.Helper()
	s := session.New(cfg, p)
	op, err := s.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { op.Release(); s.Discard() })
	return s, op
}
func rawStatementOperation(t *testing.T, mode config.Mode, serve func(context.Context, *wire.PacketIO)) (*session.Session, *session.Operation) {
	t.Helper()
	raw, err := testpeer.PeerDial(t, serve)(context.Background(), "tcp", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	return statementOperation(t, outputTestConfig(), &session.Negotiated{Raw: raw, Conn: raw, Packets: wire.NewPacketIO(raw, 4096), Capabilities: wire.ClientProtocol41, Mode: mode, OBVersion: "4.3.5.1"})
}
func streamSource(value *oceanbase.LongData) *streaminput.Source {
	if value == nil {
		return nil
	}
	return &streaminput.Source{Reader: value.Reader, Length: value.Length, MaxBytes: value.MaxBytes, Text: value.Text, State: new(atomic.Bool)}
}
func interpolateStatement(t testing.TB, cfg config.Config, mode config.Mode, query string, args []driver.NamedValue) ([]byte, error) {
	t.Helper()
	_, op := statementOperation(t, cfg, &session.Negotiated{Mode: mode})
	plan, err := sqltext.BuildBindPlanEscapes(query, mode, false)
	if err != nil {
		return nil, err
	}
	values, err := parameters.Bind(plan, args)
	if err != nil {
		return nil, err
	}
	return statement.Interpolate(op, plan, values)
}
func TestOraclePrepareMetadataPreservesTransaction(t *testing.T) {
	_, op := rawStatementOperation(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadCommand(ctx, t, p, append([]byte{22}, "SELECT ? FROM dual"...)) {
			return
		}
		header := []byte{0, 88, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0}
		if !testpeer.SendPackets(ctx, t, p, header, testpeer.TextColumn("p", 253, 45, 0), []byte{254, 0, 0, 3, 0}) {
			return
		}
		if !testpeer.ReadCommand(ctx, t, p, []byte{25, 88, 0, 0, 0}) {
			return
		}
	})
	op.SetOK(wire.OK{Status: 5})
	op.ManagedTransaction(true)
	key := op.Epoch()
	st, err := statement.Prepare(context.Background(), op, "SELECT ? FROM dual")
	if err != nil {
		t.Fatal(err)
	}
	if op.Epoch() != key || op.Protocol().OK.Status&3 != 1 {
		t.Error("prepare metadata changed the transaction")
	}
	if err = st.Close(context.Background(), op); err != nil {
		t.Fatal(err)
	}
}
