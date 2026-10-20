package publictest

import (
	"context"
	"fmt"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/metadata"
	"github.com/cocosip/go-oceanbase/types"
)

// ServeDescriptorRows serves a bounded dictionary result for one exact type name.
func ServeDescriptorRows(ctx context.Context, t *testing.T, p *wire.PacketIO, kind metadata.MetadataKind, schema, name string, rows [][]string) bool {
	t.Helper()
	plan, err := catalog.Plan(config.ModeOracle, "", metadata.MetadataFilter{Kind: kind, SchemaPattern: catalog.ExactDescriptorPattern(schema), NamePattern: catalog.ExactDescriptorPattern(name)})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Session: config.SessionOptions{Charset: "utf8mb4"}, Limits: config.Limits{MaxMessageBytes: 1 << 20}}
	s := session.New(cfg, &session.Negotiated{Mode: config.ModeOracle})
	op, err := s.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	query, err := command.Interpolate(op, plan.SQL, plan.Args)
	op.Release()
	if err != nil {
		t.Fatal(err)
	}
	if !testpeer.ReadQuery(ctx, t, p, string(query)) || !testpeer.SendPackets(ctx, t, p, []byte{byte(len(plan.Columns))}) {
		return false
	}
	for _, column := range plan.Columns {
		if !testpeer.SendPackets(ctx, t, p, testpeer.TextColumn(column.Name, 253, 45, 0)) {
			return false
		}
	}
	if !testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, 6, 0}) {
		return false
	}
	for _, row := range rows {
		var data []byte
		for _, value := range row {
			data = wire.AppendComplexLenBytes(data, []byte(value))
		}
		if !testpeer.SendPackets(ctx, t, p, data) {
			return false
		}
	}
	return testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, 6, 0})
}

// ServeComplexDictionaryFields serves the object identity and its ordered attributes.
func ServeComplexDictionaryFields(ctx context.Context, t *testing.T, p *wire.PacketIO, typ string, fields []types.Attribute) bool {
	t.Helper()
	rows := [][]string{{"", "S", typ, valueformat.TypeNameObject, fmt.Sprint(len(fields)), "1", "1"}}
	if !ServeDescriptorRows(ctx, t, p, metadata.MetadataTypes, "S", typ, rows) {
		return false
	}
	rows = nil
	for i, field := range fields {
		owner := ""
		if !valuecodec.ComplexBuiltinType(field.Type) {
			owner = "S"
		}
		rows = append(rows, []string{"", "S", typ, field.Name, fmt.Sprint(i + 1), field.Type, owner, "", "", ""})
	}
	return ServeDescriptorRows(ctx, t, p, metadata.MetadataTypeAttributes, "S", typ, rows)
}

// ServeComplexDictionary serves the numeric object used by complex value fixtures.
func ServeComplexDictionary(ctx context.Context, t *testing.T, p *wire.PacketIO, fields []string) bool {
	t.Helper()
	attrs := make([]types.Attribute, len(fields))
	for i, name := range fields {
		attrs[i] = types.Attribute{Name: name, Type: valueformat.TypeNameNumber}
	}
	return ServeComplexDictionaryFields(ctx, t, p, "T", attrs)
}
