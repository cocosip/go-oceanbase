package valuecodec_test

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestFloat64SQLParameterTypes(t *testing.T) {
	maxFloat := math.MaxFloat64
	inputs := []struct {
		value       any
		bits        uint64
		null, reuse bool
	}{
		{maxFloat, 0x7fefffffffffffff, false, false},
		{-maxFloat, 0xffefffffffffffff, false, true},
		{math.SmallestNonzeroFloat64, 1, false, true},
		{math.Copysign(0, -1), 0x8000000000000000, false, true},
		{&maxFloat, 0x7fefffffffffffff, false, true},
		{sql.NullFloat64{Float64: maxFloat, Valid: true}, 0x7fefffffffffffff, false, true},
		{(*float64)(nil), 0, true, false},
		{sql.NullFloat64{}, 0, true, true},
		{maxFloat, 0x7fefffffffffffff, false, false},
	}
	for _, mode := range []oceanbase.Mode{oceanbase.ModeOracle, oceanbase.ModeMySQL} {
		for _, explicit := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/prepared=%t", mode, explicit), func(t *testing.T) {
				const query = "INSERT INTO t (v) VALUES (?)"
				status, typ := byte(2), []byte{5, 0}
				if mode == oceanbase.ModeOracle {
					status, typ = 6, []byte{1, 1}
				}
				cfg := oceanbase.Config{Mode: mode, Execution: oceanbase.ExecutionOptions{StatementCacheSize: 1, ReuseParameterTypes: true}}
				db := publictest.ConfiguredDB(t, cfg, func(ctx context.Context, p *wire.PacketIO) {
					if !publictest.ServePrepareStatus(ctx, t, p, query, 7, 1, nil, status) {
						return
					}
					for _, input := range inputs {
						packet := append(publictest.ExecuteHeader(7), 0, 1)
						if input.null {
							packet[10] = 1
						}
						if input.reuse {
							packet[11] = 0
						} else if input.null {
							packet = append(packet, 6, 0)
						} else {
							packet = append(packet, typ...)
						}
						if !input.null {
							packet = binary.LittleEndian.AppendUint64(packet, input.bits)
						}
						if !testpeer.ServeOK(ctx, t, p, packet, status) {
							return
						}
					}
					if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
						return
					}
					p.ResetSequence()
					testpeer.ServeQuit(t, p)
				})
				defer func() { _ = db.Close() }()
				exec := func(value any) (sql.Result, error) { return db.ExecContext(t.Context(), query, value) }
				if explicit {
					stmt, err := db.PrepareContext(t.Context(), query)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = stmt.Close() }()
					exec = func(value any) (sql.Result, error) { return stmt.ExecContext(t.Context(), value) }
				}
				for i, input := range inputs {
					if _, err := exec(input.value); err != nil {
						t.Fatalf("execution %d: %v", i, err)
					}
				}
			})
		}
	}
}
