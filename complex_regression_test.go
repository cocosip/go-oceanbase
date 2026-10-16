package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestComplexOutputColumnMismatch(t *testing.T) {
	c := &conn{cfg: outputTestConfig(), session: &handshakeSession{mode: ModeOracle}}
	column := wire.Column{Type: 160, ComplexSchema: "S", ComplexName: "T"}
	key := descriptorKey{ModeOracle, "S", "T", 0}
	c.cacheDescriptor(key, &typeDescriptor{key: key, shape: &wire.ComplexShape{Fields: []*wire.ComplexShape{nil}}, fields: []descriptorField{{name: "N", typ: typeNameNumber}}})
	s := &outputState{bindings: []*outBinding{{}}, physical: []int{0}, complexColumns: []wire.Column{column, column}, complexRows: [][]byte{{0, 0, 0, 1, '7', 0, 1, '8'}}}
	if err := s.decodeComplexLocked(context.Background(), c); !errors.Is(err, ErrOut) {
		t.Fatal(err)
	}
	if s.values != nil || s.columns != nil {
		t.Fatal("invalid OUT response published values or columns")
	}
}

func TestComplexMaterializationBudget(t *testing.T) {
	c := &conn{cfg: outputTestConfig(), session: &handshakeSession{mode: ModeOracle}}
	c.cfg.Limits.MaxMessageBytes = 1024
	key := descriptorKey{ModeOracle, "S", "T", 0}
	d := &typeDescriptor{key: key, shape: &wire.ComplexShape{}}
	data := []byte{0}
	for _, name := range []string{"A", "B", "C"} {
		d.fields = append(d.fields, descriptorField{name: name, typ: typeNameBlob})
		d.shape.Fields = append(d.shape.Fields, nil)
		data = complexLenBytes(data, lobInlineFixture(strings.Repeat("x", 512)))
	}
	c.cacheDescriptor(key, d)
	if _, err := c.complexValueLocked(context.Background(), wire.Column{Type: 160, ComplexSchema: "S", ComplexName: "T"}, data); !errors.Is(err, wire.ErrMessageTooLarge) {
		t.Fatal(err)
	}
	decoded, _, err := wire.DecodeComplex(data, d.shape, maxComplexNodes)
	if err != nil {
		t.Fatal(err)
	}
	budget := c.cfg.Limits.MaxMessageBytes
	if _, err = c.decodeComplexValueLocked(context.Background(), d, decoded, 0, &budget); !errors.Is(err, wire.ErrMessageTooLarge) {
		t.Fatal("tree materialization exceeded its shared budget before JSON encoding", err)
	}
}

func TestComplexArrayUnsupportedHeader(t *testing.T) {
	for _, typ := range []string{typeNameNVarchar2, typeNameNChar, typeNameTimestamp, typeNameBlob, typeNameClob, typeNameNClob} {
		for _, elements := range [][]any{nil, {nil}} {
			t.Run(typ+"/"+strings.Repeat("null", len(elements)), func(t *testing.T) {
				c := &conn{cfg: outputTestConfig(), session: &handshakeSession{mode: ModeOracle}}
				key := descriptorKey{ModeOracle, "S", "TA", 0}
				c.cacheDescriptor(key, &typeDescriptor{key: key, array: true, fields: []descriptorField{{typ: typ}}})
				p, err := c.encodeParameterLocked(context.Background(), Array{Schema: "S", Type: "TA", Elements: elements}, 4096)
				if !errors.Is(err, ErrUnsupported) || p.Type != 0 || p.Data != nil {
					t.Fatal(p, err)
				}
				c.responseActive = true
				_, err = c.encodeParameterLocked(context.Background(), Array{ElementType: typ, Elements: elements}, 4096)
				if !errors.Is(err, ErrUnsupported) {
					t.Fatal("unsupported built-in element attempted a descriptor query", err)
				}
			})
		}
	}
}

func TestComplexDrainLaterRowset(t *testing.T) {
	for _, operation := range []string{"rows-close", "exec"} {
		t.Run(operation, func(t *testing.T) {
			const query = "CALL complex_fixture()"
			c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
				if !servePrepare(ctx, t, p, query, 23, 0, nil) || !readCommand(ctx, t, p, executeHeader(23)) {
					return
				}
				row := complexLenBytes([]byte{0, 0, 0}, lobInlineFixture(testLOBABC))
				if !sendPackets(ctx, t, p, []byte{1}, textColumn("id", 8, 63, 0), []byte{254, 0, 0, 6, 0}, []byte{0, 0, 1, 0, 0, 0, 0, 0, 0, 0}, []byte{254, 0, 0, 14, 0}, []byte{1}, complexColumnFixture(1), []byte{254, 0, 0, 6, 0}, row, []byte{254, 0, 0, 6, 0}) || !serveComplexDictionaryFields(ctx, t, p, "T", []Attribute{{Name: "B", Type: typeNameBlob}}) {
					return
				}
				if !readCommand(ctx, t, p, stmtPacket(25, 23)) || !serveOK(ctx, t, p, []byte{14}, 6) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			c.cfg.Limits.MaxMessageBytes = 1 << 16
			defer func() { _ = c.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			s, err := c.PrepareContext(ctx, query)
			if err != nil {
				t.Fatal(err)
			}
			if operation == "exec" {
				_, err = s.(driver.StmtExecContext).ExecContext(ctx, nil)
			} else {
				var rows driver.Rows
				rows, err = s.(driver.StmtQueryContext).QueryContext(ctx, nil)
				if err == nil {
					err = rows.Close()
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			if err = c.Ping(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestComplexOutputCloseDeadline(t *testing.T) {
	c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
		want, err := wire.EncodePrepareExecute(0, []byte("BEGIN p(?); END;"), []wire.Parameter{{Type: 6}}, 4096)
		if err != nil || !readCommand(ctx, t, p, want) || !sendPackets(ctx, t, p,
			combinedHeader(24, 1, 1, 0), textColumn("p", 6, 63, 0), []byte{254, 0, 0, 6, 0},
			textColumn("ordinary", 8, 63, 0), []byte{254, 0, 0, 6, 0}, []byte{0, 0, 1, 0, 0, 0, 0, 0, 0, 0}, []byte{254, 0, 0, 6, 0},
			[]byte{0, 0, 0, 14, 0, 0, 0}, []byte{1}, complexColumnFixture(1), []byte{254, 0, 0, 6, 16}, []byte{0, 0, 0, 1, '7'}, []byte{254, 0, 0, 6, 0}) || !readCommand(ctx, t, p, stmtPacket(25, 24)) {
			return
		}
		p.ResetSequence()
		if _, err = p.ReadMessage(ctx); err != nil {
			t.Error("missing descriptor query", err)
			return
		}
		// Leave the descriptor query unanswered until the client closes its transport.
		peerCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()
		_, _ = p.ReadMessage(peerCtx)
	})
	c.cfg.Limits.CleanupTimeout = 50 * time.Millisecond
	c.cfg.Limits.MaxMessageBytes = 1 << 16
	defer func() { _ = c.Close() }()
	v := Object{Type: testComplexUnchanged}
	rows, err := c.QueryContext(context.Background(), "BEGIN p(?); END;", []driver.NamedValue{{Ordinal: 1, Value: sql.Out{Dest: &v}}})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err = rows.Close()
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second || c.IsValid() || v.Type != testComplexUnchanged {
		t.Fatal(err, time.Since(started), c.IsValid(), v)
	}
}

func TestDescriptorBufferedValueRefresh(t *testing.T) {
	c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
		if !serveComplexDictionary(ctx, t, p, []string{"A", "B"}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	c.cfg.Limits.MaxMessageBytes = 1 << 16
	defer func() { _ = c.Close() }()
	key := descriptorKey{ModeOracle, "S", "T", 1}
	c.cacheDescriptor(key, &typeDescriptor{key: key, shape: &wire.ComplexShape{Fields: []*wire.ComplexShape{nil}}, fields: []descriptorField{{name: "A", typ: typeNameNumber}}})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.lock(ctx); err != nil {
		t.Fatal(err)
	}
	value, err := c.complexValueLocked(ctx, wire.Column{Type: 160, ComplexSchema: "S", ComplexName: "T", ComplexVersion: 1}, []byte{0, 1, '7', 1, '8'})
	c.unlock()
	var got Object
	if err != nil {
		t.Fatal(err)
	}
	if err = got.Scan(value); err != nil || len(got.Fields) != 2 || got.Fields[1].Name != "B" || got.Fields[1].Value != Number("8") {
		t.Fatal(got, err)
	}
}
