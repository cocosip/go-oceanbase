package valuecodec_test

import (
	"context"
	"encoding/binary"
	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"testing"
)

func TestColumnTypes(t *testing.T) {
	for _, tt := range []struct {
		name                     string
		mode                     oceanbase.Mode
		column                   wire.Column
		length, precision, scale int64
		lengthOK, decimalOK      bool
	}{
		{"utf8 characters", oceanbase.ModeMySQL, wire.Column{Type: 253, Charset: 45, Length: 40}, 10, 0, 0, true, false},
		{"gb18030 2022 characters", oceanbase.ModeMySQL, wire.Column{Type: 253, Charset: 217, Length: 40}, 10, 0, 0, true, false},
		{"hkscs characters", oceanbase.ModeMySQL, wire.Column{Type: 253, Charset: 152, Length: 40}, 20, 0, 0, true, false},
		{"hkscs31 characters", oceanbase.ModeMySQL, wire.Column{Type: 253, Charset: 153, Length: 40}, 20, 0, 0, true, false},
		{"binary bytes", oceanbase.ModeMySQL, wire.Column{Type: 252, Charset: 63, Length: 400}, 400, 0, 0, true, false},
		{"unknown collation", oceanbase.ModeMySQL, wire.Column{Type: 253, Charset: 999, Length: 40}, 0, 0, 0, false, false},
		{"nonintegral length", oceanbase.ModeMySQL, wire.Column{Type: 253, Charset: 45, Length: 41}, 0, 0, 0, false, false},
		{"decimal sign and dot", oceanbase.ModeMySQL, wire.Column{Type: 246, Length: 12, Decimals: 2}, 0, 10, 2, false, true},
		{"unsigned decimal", oceanbase.ModeMySQL, wire.Column{Type: 246, Length: 11, Decimals: 2, Flags: 32}, 0, 10, 2, false, true},
		{"unknown decimal", oceanbase.ModeMySQL, wire.Column{Type: 246, Decimals: 31}, 0, 0, 0, false, false},
		{"invalid decimal precision", oceanbase.ModeMySQL, wire.Column{Type: 246, Length: 100, Decimals: 2}, 0, 0, 0, false, false},
		{"invalid oracle scale", oceanbase.ModeOracle, wire.Column{Type: 246, Precision: 10, Decimals: 128}, 0, 0, 0, false, false},
		{"oracle float binary precision", oceanbase.ModeOracle, wire.Column{Type: 206, Precision: 24}, 0, 0, 0, false, false},
		{"oracle float scale marker", oceanbase.ModeOracle, wire.Column{Type: 206, Precision: 24, Decimals: 129}, 0, 0, 0, false, false},
		{"oracle negative scale", oceanbase.ModeOracle, wire.Column{Type: 246, Precision: 10, Decimals: 254}, 0, 10, -2, false, true},
		{"oracle unconstrained number", oceanbase.ModeOracle, wire.Column{Type: 246, Length: 40}, 0, 0, 0, false, false},
		{"oracle varchar characters", oceanbase.ModeOracle, wire.Column{Type: 253, Charset: 45, Length: 40}, 40, 0, 0, true, false},
		{"oracle nchar", oceanbase.ModeOracle, wire.Column{Type: 208, Charset: 54, Length: 40}, 10, 0, 0, true, false},
		{"integer no string length", oceanbase.ModeMySQL, wire.Column{Type: 3, Length: 11}, 0, 0, 0, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			status := byte(2)
			if tt.mode == oceanbase.ModeOracle {
				status = 6
			}
			packet := testpeer.TextColumn("v", tt.column.Type, tt.column.Charset, tt.column.Flags)
			binary.LittleEndian.PutUint32(packet[len(packet)-10:], tt.column.Length)
			packet[len(packet)-3], packet[len(packet)-2] = tt.column.Decimals, tt.column.Precision
			db := publictest.ConfiguredDB(t, oceanbase.Config{Mode: tt.mode}, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ReadQuery(ctx, t, p, "SELECT v FROM t") || !testpeer.SendPackets(ctx, t, p, []byte{1}, packet, []byte{254, 0, 0, status, 0}, []byte{254, 0, 0, status, 0}) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			rows, err := db.QueryContext(t.Context(), "SELECT v FROM t")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rows.Close() }()
			columns, err := rows.ColumnTypes()
			if err != nil || len(columns) != 1 {
				t.Fatal("column metadata missing", columns, err)
			}
			if n, known := columns[0].Length(); n != tt.length || known != tt.lengthOK {
				t.Fatalf("length=%d,%v", n, known)
			}
			if p, s, known := columns[0].DecimalSize(); p != tt.precision || s != tt.scale || known != tt.decimalOK {
				t.Fatalf("decimal=%d,%d,%v", p, s, known)
			}
		})
	}
}
