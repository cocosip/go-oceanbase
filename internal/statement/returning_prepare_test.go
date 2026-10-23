package statement

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const returningInsertSQL = "INSERT INTO t(v) VALUES (?) RETURNING id INTO ?"

func TestPrepareOracleReturningMetadata(t *testing.T) {
	for _, query := range []string{
		returningInsertSQL,
		"UPDATE t SET v=? RETURNING id INTO ?",
		"DELETE FROM t WHERE v=? RETURNING id INTO ?",
	} {
		t.Run(query, func(t *testing.T) {
			done := make(chan struct{})
			defer func() { <-done }()
			ctx, op := protocolOperation(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
				defer close(done)
				if !testpeer.ReadCommand(ctx, t, p, append([]byte{22}, query...)) {
					return
				}
				if testpeer.SendPackets(ctx, t, p, []byte{0, 7, 0, 0, 0, 1, 0, 1, 0, 0, 0, 0},
					testpeer.TextColumn("?", 8, 63, 0), []byte{254, 0, 0, 6, 0},
					testpeer.TextColumn("ID", 246, 63, 0), []byte{254, 0, 0, 6, 0}) {
					p.ResetSequence()
					if data, err := p.ReadMessage(ctx); err == nil && !bytes.Equal(data, []byte{25, 7, 0, 0, 0}) {
						t.Errorf("unexpected statement close: %x", data)
					}
				}
			})
			s, err := Prepare(ctx, op, query)
			if err != nil {
				t.Fatal(err)
			}
			if s.Plan().NumInput != 2 || len(s.Parameters()) != 2 || s.Parameters()[1].Name != "ID" || s.Parameters()[1].Type != 246 {
				t.Fatalf("returning metadata lost: plan=%+v parameters=%+v", s.Plan(), s.Parameters())
			}
			if err := s.Close(ctx, op); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPrepareOracleReturningRejectsWrongCounts(t *testing.T) {
	for _, tc := range []struct {
		query           string
		inputs, columns byte
	}{
		{returningInsertSQL, 0, 1},
		{returningInsertSQL, 1, 2},
		{"SELECT ? FROM dual", 0, 1},
		{"BEGIN INSERT INTO t(v) VALUES (?) RETURNING id INTO ?; END;", 1, 1},
	} {
		t.Run(tc.query, func(t *testing.T) {
			done := make(chan struct{})
			defer func() { <-done }()
			ctx, op := protocolOperation(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
				defer close(done)
				if !testpeer.ReadCommand(ctx, t, p, append([]byte{22}, tc.query...)) {
					return
				}
				packets := [][]byte{{0, 7, 0, 0, 0, tc.columns, 0, tc.inputs, 0, 0, 0, 0}}
				for i := byte(0); i < tc.inputs; i++ {
					packets = append(packets, testpeer.TextColumn("?", 8, 63, 0))
				}
				if tc.inputs > 0 {
					packets = append(packets, []byte{254, 0, 0, 6, 0})
				}
				for i := byte(0); i < tc.columns; i++ {
					packets = append(packets, testpeer.TextColumn("ID", 246, 63, 0))
				}
				if tc.columns > 0 {
					packets = append(packets, []byte{254, 0, 0, 6, 0})
				}
				testpeer.SendPackets(ctx, t, p, packets...)
			})
			if _, err := Prepare(ctx, op, tc.query); !errors.Is(err, dberrors.ErrBind) || !op.Closed() || op.ResponseActive() {
				t.Fatalf("bad counts accepted: error=%v closed=%t", err, op.Closed())
			}
		})
	}
}
