package oceanbase

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestBinaryRows(t *testing.T) {
	query := "SELECT values_from_fixture"
	columns := [][]byte{textColumn("n", 6, 63, 0), textColumn("v", 8, 63, 0), textColumn("u", 8, 63, 32), textColumn("d", 246, 45, 0), textColumn("f", 5, 63, 0), textColumn("b", 252, 63, 0), textColumn("n6", 253, 45, 0), textColumn("n7", 253, 45, 0), textColumn("s", 253, 45, 0), textColumn("n9", 253, 45, 0)}
	row := []byte{0, 4, 1 | 2 | 8}
	row = binary.LittleEndian.AppendUint64(row, uint64(42))
	row = binary.LittleEndian.AppendUint64(row, math.MaxUint64)
	row = append(row, 21)
	row = append(row, "12345678901234567.890"...)
	row = binary.LittleEndian.AppendUint64(row, math.Float64bits(1.25))
	row = append(row, 2, 0, 255, 0)
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, query, 17, 0, columns) {
			return
		}
		if !readCommand(ctx, t, p, executeHeader(17)) {
			return
		}
		if !sendPackets(ctx, t, p, []byte{byte(len(columns))}) {
			return
		}
		for _, c := range columns {
			if !sendPackets(ctx, t, p, c) {
				return
			}
		}
		if !sendPackets(ctx, t, p, []byte{254, 0, 0, 2, 0}, row, []byte{254, 0, 0, 2, 0}) {
			return
		}
		if !readCommand(ctx, t, p, stmtPacket(25, 17)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	s, err := db.PrepareContext(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	var n, n6, n7, n9 sql.NullString
	var value int64
	var u, d, text string
	var f float64
	var b []byte
	if err = s.QueryRowContext(context.Background()).Scan(&n, &value, &u, &d, &f, &b, &n6, &n7, &text, &n9); err != nil {
		t.Fatal(err)
	}
	if n.Valid || n6.Valid || n7.Valid || n9.Valid || value != 42 || u != testMaxUint64Text || d != "12345678901234567.890" || f != 1.25 || len(b) != 2 || b[1] != 255 || text != "" {
		t.Fatal(n, value, u, d, f, b, n6, n7, text, n9)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBinaryFloatModes(t *testing.T) {
	data := binary.LittleEndian.AppendUint64(nil, math.Float64bits(math.Inf(1)))
	if _, err := decodeBinaryValue(wire.Column{Type: 5}, ModeMySQL, data); err == nil {
		t.Fatal("MySQL nonfinite accepted")
	}
	v, err := decodeBinaryValue(wire.Column{Type: 5}, ModeOracle, data)
	if err != nil || !math.IsInf(v.(float64), 1) {
		t.Fatal(v, err)
	}
}

func TestBinaryTimeAndScalars(t *testing.T) {
	for _, tc := range []struct {
		typ  byte
		data []byte
		want any
	}{
		{1, []byte{255}, int64(-1)}, {2, []byte{0, 128}, int64(-32768)},
		{3, []byte{255, 255, 255, 127}, int64(2147483647)},
		{10, []byte{}, "0000-00-00"}, {12, []byte{}, "0000-00-00 00:00:00"},
		{10, []byte{233, 7, 10, 17}, "2025-10-17"},
		{12, []byte{233, 7, 10, 17, 12, 34, 56, 64, 226, 1, 0}, "2025-10-17 12:34:56.123456"},
		{11, []byte{1, 2, 0, 0, 0, 3, 4, 5, 1, 0, 0, 0}, "-51:04:05.000001"},
	} {
		v, err := decodeBinaryValue(wire.Column{Type: tc.typ}, ModeMySQL, tc.data)
		if err != nil || !reflect.DeepEqual(v, tc.want) {
			t.Fatalf("type=%d got %v want %v: %v", tc.typ, v, tc.want, err)
		}
	}
	for _, tc := range []struct {
		typ  byte
		data []byte
	}{
		{10, []byte{1}}, {12, []byte{233, 7, 1, 1, 24, 0, 0}},
		{11, []byte{2, 0, 0, 0, 0, 0, 0, 0}}, {12, []byte{233, 7, 1, 1, 0, 0, 0, 64, 66, 15, 0}},
	} {
		if _, err := decodeBinaryValue(wire.Column{Type: tc.typ}, ModeMySQL, tc.data); err == nil {
			t.Fatal("invalid temporal accepted", tc)
		}
	}
	local := time.FixedZone("test", 8*3600)
	value := time.Date(2025, 10, 17, 4, 34, 56, 123456000, time.UTC)
	p, err := encodeParameter(value, ModeMySQL, local, 1024)
	want := []byte{11, 233, 7, 10, 17, 12, 34, 56, 64, 226, 1, 0}
	if err != nil || p.Type != 12 || !reflect.DeepEqual(p.Data, want) {
		t.Fatal(p, err)
	}
	if _, err = encodeParameter(value.Add(time.Nanosecond), ModeMySQL, local, 1024); !errors.Is(err, ErrBind) {
		t.Fatal("unrepresentable precision silently truncated", err)
	}
	if _, err = encodeParameter("oversized", ModeMySQL, local, 3); !errors.Is(err, wire.ErrMessageTooLarge) {
		t.Fatal(err)
	}
}
