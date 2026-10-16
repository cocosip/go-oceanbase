package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const (
	testComplexUnchanged = "OLD"
	testComplexAlias     = "ALIAS"
)

func TestComplexOutputs(t *testing.T) {
	for _, tailError := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "tail-error"}[tailError], func(t *testing.T) {
			db := outOracleDB(t, PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
				want, err := wire.EncodePrepareExecute(0, []byte("BEGIN p(?); END;"), []wire.Parameter{{Type: 6}}, 4096)
				if err != nil || !readCommand(ctx, t, p, want) {
					return
				}
				if !sendPackets(ctx, t, p, combinedHeader(21, 1, 1, 1), textColumn("p", 6, 63, 0), []byte{254, 0, 0, 6, 0}, complexColumnFixture(7), []byte{254, 0, 0, 6, 16}, []byte{0, 0, 0, 1, '7'}, []byte{254, 0, 0, 6, 0}) {
					return
				}
				tail := []byte{0, 0, 0, 6, 0, 0, 0}
				if tailError {
					tail = []byte{255, 1, 0, '#', 'H', 'Y', '0', '0', '0', 'x'}
				}
				if !sendPackets(ctx, t, p, tail) || !readCommand(ctx, t, p, stmtPacket(25, 21)) {
					return
				}
				if !tailError && !serveComplexDictionary(ctx, t, p, []string{"N"}) {
					return
				}
				if !serveOK(ctx, t, p, []byte{14}, 6) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			v := Object{Type: testComplexUnchanged}
			_, err := db.ExecContext(ctx, "BEGIN p(?); END;", sql.Out{Dest: &v})
			if tailError {
				var server *Error
				if !errors.As(err, &server) || v.Type != testComplexUnchanged {
					t.Fatal(v, err)
				}
			} else if err != nil || v.Type != "T" || v.Fields[0].Value != Number("7") {
				t.Fatal(v, err)
			}
			if err = db.PingContext(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestComplexNestedLOB(t *testing.T) {
	const query = "SELECT v FROM complex_fixture"
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, query, 22, 0, [][]byte{complexColumnFixture(1)}) || !readCommand(ctx, t, p, executeHeader(22)) {
			return
		}
		row := []byte{0, 0, 0, 0}
		row = append(row, byte(len(lobInlineFixture(testLOBABC))))
		row = append(row, lobInlineFixture(testLOBABC)...)
		if !sendPackets(ctx, t, p, []byte{1}, complexColumnFixture(1), []byte{254, 0, 0, 6, 0}, row, []byte{254, 0, 0, 6, 0}) || !serveComplexDictionaryFields(ctx, t, p, "T", []Attribute{{Name: "CH", Type: "SUB"}}) || !serveComplexDictionaryFields(ctx, t, p, "SUB", []Attribute{{Name: "B", Type: typeNameBlob}}) {
			return
		}
		if !readCommand(ctx, t, p, stmtPacket(25, 22)) {
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
	var v Object
	if err = s.QueryRowContext(ctx).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v.Fields[0].Value.(Object).Fields[0].Value, Raw(testLOBABC)) {
		t.Fatal(v)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDescriptorSynonymAndBoundary(t *testing.T) {
	c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
		if !serveDescriptorRows(ctx, t, p, MetadataTypes, "S", testComplexAlias, nil) || !serveDescriptorRows(ctx, t, p, MetadataSynonyms, "S", testComplexAlias, [][]string{{"", "S", testComplexAlias, "S", "T", ""}}) || !serveComplexDictionary(ctx, t, p, []string{"N"}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	c.cfg.Limits.MaxMessageBytes = 1 << 16
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.lock(ctx); err != nil {
		t.Fatal(err)
	}
	key := descriptorKey{ModeOracle, "S", testComplexAlias, 9}
	c.responseActive = true
	_, err := c.descriptorLocked(ctx, key, 0, map[descriptorKey]bool{})
	if !errors.Is(err, errResponseActive) {
		t.Fatal(err)
	}
	c.responseActive = false
	d, err := c.descriptorLocked(ctx, key, 0, map[descriptorKey]bool{})
	if err != nil || d.key.name != "T" || d.key.version != 9 {
		t.Fatal(d, err)
	}
	got, err := c.descriptorLocked(ctx, key, 0, map[descriptorKey]bool{})
	if err != nil || got != d {
		t.Fatal(got, err)
	}
	c.unlock()
}

func TestComplexAnonymousArray(t *testing.T) {
	c := &conn{session: &handshakeSession{mode: ModeOracle}, cfg: outputTestConfig()}
	v := Array{ElementType: typeNameNumber, Elements: []any{Number("1.20"), nil}}
	p, err := c.encodeParameterLocked(context.Background(), v, 4096)
	if err != nil || !reflect.DeepEqual(p.TypeMetadata, []byte{0, 0, 0, 0}) || !reflect.DeepEqual(p.Data, []byte{2, 2, 4, '1', '.', '2', '0'}) {
		t.Fatal(p, err)
	}
	column := wire.Column{Type: 160, ElementType: 0}
	value, err := c.complexValueLocked(context.Background(), column, []byte{2, 8, 4, '1', '.', '2', '0'})
	var got Array
	if err != nil {
		t.Fatal(err)
	}
	if err = got.Scan(value); err != nil || !reflect.DeepEqual(got, v) {
		t.Fatal(got, err)
	}
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		c.session.mode = mode
		if mode == ModeMySQL {
			if _, err = c.encodeParameterLocked(context.Background(), v, 4096); !errors.Is(err, ErrUnsupported) {
				t.Fatal(err)
			}
		} else if _, err = c.clientLiteral(v, 4096); err == nil {
			t.Fatal("complex JSON interpolated into SQL")
		}
	}
}

func TestComplexNamedArray(t *testing.T) {
	c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
		if !serveDescriptorRows(ctx, t, p, MetadataTypes, "S", "TA", [][]string{{"", "S", "TA", "COLLECTION", "0", "1", "1"}}) || !serveDescriptorRows(ctx, t, p, MetadataCollectionElements, "S", "TA", [][]string{{"", "S", "TA", "VARRAY", "", typeNameNumber, "", "", "", ""}}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	c.cfg.Limits.MaxMessageBytes = 1 << 16
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.lock(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := c.encodeParameterLocked(ctx, Array{Schema: "S", Type: "TA", Elements: []any{Number("1.20"), nil}}, 4096)
	c.unlock()
	if err != nil || !reflect.DeepEqual(p.TypeMetadata, []byte{0, 0, 0, 0}) {
		t.Fatal(p, err)
	}
}

func TestComplexCharset(t *testing.T) {
	c := &conn{cfg: outputTestConfig(), session: &handshakeSession{mode: ModeOracle}}
	c.cfg.Session.Charset = "gb18030"
	data, err := charset.Encode("gb18030", "中文")
	if err != nil {
		t.Fatal(err)
	}
	budget := c.cfg.Limits.MaxMessageBytes
	v, err := c.decodeComplexFieldLocked(context.Background(), descriptorField{typ: typeNameVarchar2}, wire.ComplexValue{Data: data}, 0, &budget)
	if err != nil || v != "中文" {
		t.Fatal(v, err)
	}
	v, err = c.decodeComplexFieldLocked(context.Background(), descriptorField{typ: typeNameClob}, wire.ComplexValue{Data: lobInlineFixture(string(data))}, 0, &budget)
	if err != nil || v != "中文" {
		t.Fatal("nested CLOB charset", v, err)
	}
}

func TestDescriptorBudget(t *testing.T) {
	fields := make([]string, 40)
	for i := range fields {
		fields[i] = fmt.Sprintf("F%d", i)
	}
	c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
		if !serveComplexDictionary(ctx, t, p, fields) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	c.cfg.Limits.MaxMessageBytes = 10500
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.lock(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := c.descriptorLocked(ctx, descriptorKey{ModeOracle, "S", "T", 0}, 0, map[descriptorKey]bool{})
	c.unlock()
	if !errors.Is(err, wire.ErrMessageTooLarge) || !c.IsValid() {
		t.Fatal(err, c.IsValid(), c.descriptorBytes)
	}
}

func TestComplexNullConversion(t *testing.T) {
	var o *Object
	for _, value := range []any{o, Object{Null: true}, Array{Null: true}} {
		var named = driver.NamedValue{Value: value}
		if err := new(conn).CheckNamedValue(&named); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDescriptorPublicSynonym(t *testing.T) {
	c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
		if !serveDescriptorRows(ctx, t, p, MetadataTypes, "S", testComplexAlias, nil) ||
			!serveDescriptorRows(ctx, t, p, MetadataSynonyms, "S", testComplexAlias, nil) ||
			!serveDescriptorRows(ctx, t, p, MetadataSynonyms, "PUBLIC", testComplexAlias, [][]string{{"", "PUBLIC", testComplexAlias, "S", "T", ""}}) ||
			!serveComplexDictionary(ctx, t, p, []string{"N"}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	c.cfg.Limits.MaxMessageBytes = 1 << 16
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.lock(ctx); err != nil {
		t.Fatal(err)
	}
	d, err := c.descriptorLocked(ctx, descriptorKey{ModeOracle, "S", testComplexAlias, 0}, 0, map[descriptorKey]bool{})
	c.unlock()
	if err != nil || d.key.name != "T" || d.key.schema != "S" {
		t.Fatal(d, err)
	}
}

func TestComplexAnonymousObjectArray(t *testing.T) {
	c := &conn{cfg: outputTestConfig(), session: &handshakeSession{mode: ModeOracle}}
	key := descriptorKey{ModeOracle, "S", "T", 7}
	c.cacheDescriptor(key, &typeDescriptor{key: key, shape: &wire.ComplexShape{Fields: []*wire.ComplexShape{nil}}, fields: []descriptorField{{name: "N", typ: typeNameNumber}}})
	header := append(textColumn("v", 160, 45, 0), 0, 0, 0, 160, 1, 'S', 1, 'T', 7)
	column, err := wire.DecodeColumn(header)
	if err != nil {
		t.Fatal(err)
	}
	value, err := c.complexValueLocked(context.Background(), column, []byte{2, 8, 0, 1, '7'})
	var got Array
	if err != nil {
		t.Fatal(err)
	}
	if err = got.Scan(value); err != nil || got.Schema != "S" || got.Type != "" || got.ElementType != "T" || got.ElementVersion != 7 || len(got.Elements) != 2 || got.Elements[1] != nil {
		t.Fatal(got, err)
	}
	p, err := c.encodeParameterLocked(context.Background(), got, 4096)
	if err != nil || !reflect.DeepEqual(p.TypeMetadata, []byte{0, 0, 0, 160, 1, 'S', 1, 'T', 7}) || !reflect.DeepEqual(p.Data, []byte{2, 2, 0, 1, '7'}) {
		t.Fatal(p, err)
	}
}
