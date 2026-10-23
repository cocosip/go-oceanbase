package valuecodec

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestFloat64ParameterEncoding(t *testing.T) {
	for _, mode := range []config.Mode{config.ModeOracle, config.ModeMySQL} {
		for _, tc := range []struct {
			name string
			bits uint64
		}{
			{"ordinary", 0x3ff4000000000000},
			{"zero", 0},
			{"negative-zero", 0x8000000000000000},
			{"large", 0x6974e718d7d7625a},
			{"max", 0x7fefffffffffffff},
			{"negative-max", 0xffefffffffffffff},
			{"subnormal", 1},
			{"negative-subnormal", 0x8000000000000001},
			{"nan", 0x7ff8000000000042},
			{"positive-infinity", 0x7ff0000000000000},
			{"negative-infinity", 0xfff0000000000000},
		} {
			t.Run(fmt.Sprintf("%d/%s", mode, tc.name), func(t *testing.T) {
				value := math.Float64frombits(tc.bits)
				p, err := EncodeParameter(value, mode, config.SessionOptions{}, 1024)
				if mode == config.ModeMySQL && (math.IsNaN(value) || math.IsInf(value, 0)) {
					if !errors.Is(err, dberrors.ErrBind) {
						t.Fatalf("nonfinite parameter: %v", err)
					}
					return
				}
				wantType := uint16(5)
				if err != nil || p.Type != wantType || p.LengthEncoded || p.LongData || len(p.Data) != 8 {
					t.Fatalf("parameter=%+v error=%v, want fixed-width type %d", p, err, wantType)
				}
				if got := binary.LittleEndian.Uint64(p.Data); got != tc.bits {
					t.Fatalf("payload bits=%016x, want %016x", got, tc.bits)
				}
				packet, err := wire.EncodeStmtExecute(7, []wire.Parameter{p}, 1024)
				if err != nil || len(packet) != 22 {
					t.Fatalf("execute packet=%x error=%v", packet, err)
				}
				if packet[12] != 5 || packet[13] != 0 {
					t.Fatalf("wire type=%d flags=%d, want DOUBLE with no flags", packet[12], packet[13])
				}
				if got := binary.LittleEndian.Uint64(packet[14:]); got != tc.bits {
					t.Fatalf("wire payload bits=%016x, want %016x", got, tc.bits)
				}
			})
		}
	}
}
