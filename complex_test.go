package oceanbase

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestComplexLossless(t *testing.T) {
	want := Object{Schema: "S", Type: "T", Version: 7, Fields: []Attribute{
		{Name: "N", Type: typeNameNumber, Value: Number("12345678901234567890.00100")},
		{Name: "B", Type: typeNameRaw, Value: Raw{0, 255}},
		{Name: "D", Type: typeNameDate, Value: "2025-10-17 12:34:56"},
		{Name: "A", Type: "TA", Value: Array{Schema: "S", Type: "TA", ElementType: typeNameNumber, Elements: []any{nil, Number("1.20")}}},
		{Name: "E", Type: "TA", Value: Array{Schema: "S", Type: "TA", ElementType: typeNameNumber, Elements: []any{}}},
		{Name: "Z", Type: typeNameVarchar2},
	}}
	value, err := want.Value()
	if err != nil || !driver.IsValue(value) || !json.Valid([]byte(value.(string))) || !bytes.Contains([]byte(value.(string)), []byte("AP8=")) {
		t.Fatal(value, err)
	}
	var got Object
	if err = got.Scan(value); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(got, err)
	}
	old := got
	if err = got.Scan(`{"format":99,"kind":"object"}`); !errors.Is(err, ErrComplex) || !reflect.DeepEqual(got, old) {
		t.Fatal("invalid Scan changed destination", err)
	}
	if err = got.Scan(nil); err != nil || !got.Null {
		t.Fatal(got, err)
	}
	if v, err := got.Value(); err != nil || v != nil {
		t.Fatal(v, err)
	}
}

func complexColumnFixture(version byte) []byte {
	b := textColumn("v", 160, 45, 0)
	return append(b, 1, 'S', 1, 'T', version)
}

func serveComplexDictionary(ctx context.Context, t *testing.T, p *wire.PacketIO, fields []string) bool {
	attrs := make([]Attribute, len(fields))
	for i, name := range fields {
		attrs[i] = Attribute{Name: name, Type: typeNameNumber}
	}
	return serveComplexDictionaryFields(ctx, t, p, "T", attrs)
}
func serveComplexDictionaryFields(ctx context.Context, t *testing.T, p *wire.PacketIO, typ string, fields []Attribute) bool {
	rows := [][]string{{"", "S", typ, typeNameObject, fmt.Sprint(len(fields)), "1", "1"}}
	if !serveDescriptorRows(ctx, t, p, MetadataTypes, "S", typ, rows) {
		return false
	}
	rows = nil
	for i, f := range fields {
		owner := ""
		if !complexBuiltinType(f.Type) {
			owner = "S"
		}
		rows = append(rows, []string{"", "S", typ, f.Name, fmt.Sprint(i + 1), f.Type, owner, "", "", ""})
	}
	return serveDescriptorRows(ctx, t, p, MetadataTypeAttributes, "S", typ, rows)
}
func serveDescriptorRows(ctx context.Context, t *testing.T, p *wire.PacketIO, kind MetadataKind, schema, name string, rows [][]string) bool {
	t.Helper()
	plan, err := metadataPlan(ModeOracle, "", MetadataFilter{Kind: kind, SchemaPattern: exactDescriptorPattern(schema), NamePattern: exactDescriptorPattern(name)})
	if err != nil {
		t.Fatal(err)
	}
	c := &conn{session: &handshakeSession{mode: ModeOracle}, cfg: Config{Session: SessionOptions{Charset: charsetUTF8MB4}, Limits: Limits{MaxMessageBytes: 1 << 20}}}
	query, err := c.interpolate(plan.query, plan.args)
	if err != nil {
		t.Fatal(err)
	}
	if !readQuery(ctx, t, p, query) || !sendPackets(ctx, t, p, []byte{byte(len(plan.columns))}) {
		return false
	}
	for _, column := range plan.columns {
		if !sendPackets(ctx, t, p, textColumn(column.name, 253, 45, 0)) {
			return false
		}
	}
	if !sendPackets(ctx, t, p, []byte{254, 0, 0, 6, 0}) {
		return false
	}
	for _, row := range rows {
		var data []byte
		for _, value := range row {
			data = append(data, byte(len(value)))
			data = append(data, value...)
		}
		if !sendPackets(ctx, t, p, data) {
			return false
		}
	}
	return sendPackets(ctx, t, p, []byte{254, 0, 0, 6, 0})
}

func TestDescriptorNoReentry(t *testing.T) {
	const query = "SELECT v, id FROM complex_fixture"
	column := complexColumnFixture(7)
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, query, 17, 0, [][]byte{column, textColumn("id", 8, 63, 0)}) || !readCommand(ctx, t, p, executeHeader(17)) {
			return
		}
		row := []byte{0, 0, 0, 4, '1', '.', '2', '0', 42, 0, 0, 0, 0, 0, 0, 0}
		if !sendPackets(ctx, t, p, []byte{2}, column, textColumn("id", 8, 63, 0), []byte{254, 0, 0, 6, 0}, row, []byte{254, 0, 0, 6, 0}) {
			return
		}
		// net.Pipe blocks until the entire business response has been read.
		if !serveComplexDictionary(ctx, t, p, []string{"N"}) || !readCommand(ctx, t, p, stmtPacket(25, 17)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := db.PrepareContext(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	var value Object
	var id int64
	if err = s.QueryRowContext(ctx).Scan(&value, &id); err != nil {
		t.Fatal(err)
	}
	if id != 42 || value.Version != 7 || len(value.Fields) != 1 || value.Fields[0].Value != Number("1.20") {
		t.Fatal(value, id)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDescriptorRefresh(t *testing.T) {
	const query = "SELECT v FROM complex_fixture"
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, query, 18, 0, [][]byte{complexColumnFixture(1)}) {
			return
		}
		for version := byte(1); version <= 2; version++ {
			if !readCommand(ctx, t, p, executeHeader(18)) {
				return
			}
			row := []byte{0, 0, 0, 1, '7'}
			fields := []string{"A"}
			if version == 2 {
				row = append(row, 1, '8')
				fields = append(fields, "B")
			}
			if !sendPackets(ctx, t, p, []byte{1}, complexColumnFixture(version), []byte{254, 0, 0, 6, 0}, row, []byte{254, 0, 0, 6, 0}) || !serveComplexDictionary(ctx, t, p, fields) {
				return
			}
		}
		if !readCommand(ctx, t, p, stmtPacket(25, 18)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := db.PrepareContext(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	for version := uint64(1); version <= 2; version++ {
		var v Object
		if err = s.QueryRowContext(ctx).Scan(&v); err != nil {
			t.Fatal(err)
		}
		if v.Version != version || len(v.Fields) != int(version) {
			t.Fatal(v)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestComplexDepth(t *testing.T) {
	var v any = Object{Type: "T", Fields: []Attribute{}}
	for i := 0; i < 34; i++ {
		v = Object{Type: "T", Fields: []Attribute{{Name: "N", Type: "T", Value: v}}}
	}
	if _, err := v.(Object).Value(); !errors.Is(err, ErrComplex) {
		t.Fatal(err)
	}
	var o Object
	if err := o.Scan(strings.Repeat("{", 100)); err == nil {
		t.Fatal("malformed JSON accepted")
	}
}

func TestComplexNativeBinding(t *testing.T) {
	const query = "INSERT INTO complex_fixture VALUES (?)"
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, "INSERT INTO complex_fixture VALUES (?)", 19, 1, nil) || !serveComplexDictionary(ctx, t, p, []string{"N"}) {
			return
		}
		want := append(executeHeader(19), 0, 1, 160, 0, 1, 'S', 1, 'T', 0, 0, 4, '1', '.', '2', '0')
		if !readCommand(ctx, t, p, want) || !sendPackets(ctx, t, p, []byte{0, 1, 0, 6, 0, 0, 0}) || !readCommand(ctx, t, p, stmtPacket(25, 19)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	v := Object{Schema: "S", Type: "T", Fields: []Attribute{{Name: "N", Value: Number("1.20")}}}
	if _, err := db.ExecContext(ctx, query, v); err != nil {
		t.Fatal(err)
	}
}
