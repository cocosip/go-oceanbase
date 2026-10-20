package parameters

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/metadata"
	"github.com/cocosip/go-oceanbase/types"
)

func parameterDictionaryOperation(t *testing.T, element string) (context.Context, *session.Operation) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	cfg, err := config.Normalize(config.Config{User: testpeer.User, Mode: config.ModeOracle, Limits: config.Limits{MaxMessageBytes: 1 << 16}})
	if err != nil {
		t.Fatal(err)
	}
	client, peer := net.Pipe()
	s := session.New(cfg, &session.Negotiated{Raw: client, Conn: client, Packets: wire.NewPacketIO(client, cfg.Limits.MaxMessageBytes), Mode: config.ModeOracle, OBVersion: "4.3.5.1", Capabilities: wire.ClientProtocol41, OK: wire.OK{Status: 6}})
	op, err := s.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	entries := []struct {
		kind metadata.MetadataKind
		rows [][]string
	}{
		{metadata.MetadataTypes, [][]string{{"", "S", "TA", "COLLECTION", "0", "1", "1"}}},
		{metadata.MetadataCollectionElements, [][]string{{"", "S", "TA", "VARRAY", "", element, "", "", "", ""}}},
	}
	queries := make([][]byte, len(entries))
	for i, entry := range entries {
		plan, err := catalog.Plan(config.ModeOracle, "4.3.5.1", metadata.MetadataFilter{Kind: entry.kind, SchemaPattern: "S", NamePattern: "TA"})
		if err != nil {
			t.Fatal(err)
		}
		query, err := command.Interpolate(op, plan.SQL, plan.Args)
		if err != nil {
			t.Fatal(err)
		}
		queries[i] = append([]byte{3}, query...)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		p := wire.NewPacketIO(peer, cfg.Limits.MaxMessageBytes)
		for i, entry := range entries {
			if !testpeer.ReadCommand(ctx, t, p, queries[i]) {
				return
			}
			columns := catalog.Columns(entry.kind)
			packets := [][]byte{{byte(len(columns))}}
			for _, column := range columns {
				packets = append(packets, testpeer.TextColumn(column.Name, 253, 45, 0))
			}
			packets = append(packets, []byte{254, 0, 0, 6, 0})
			for _, row := range entry.rows {
				var data []byte
				for _, field := range row {
					data = wire.AppendComplexLenBytes(data, []byte(field))
				}
				packets = append(packets, data)
			}
			if !testpeer.SendPackets(ctx, t, p, append(packets, []byte{254, 0, 0, 6, 0})...) {
				return
			}
		}
	}()
	t.Cleanup(func() { cancel(); op.Release(); s.Discard(); _ = peer.Close(); <-done })
	return ctx, op
}

func TestComplexNamedArray(t *testing.T) {
	ctx, op := parameterDictionaryOperation(t, valueformat.TypeNameNumber)
	var resolver catalog.Resolver
	p, err := EncodeValue(ctx, op, &resolver, types.Array{Schema: "S", Type: "TA", Elements: []any{types.Number("1.20"), nil}}, 4096)
	if err != nil || !reflect.DeepEqual(p.TypeMetadata, []byte{0, 0, 0, 0}) {
		t.Fatal(p, err)
	}
}

func TestComplexArrayUnsupportedHeader(t *testing.T) {
	for _, typ := range []string{valueformat.TypeNameNVarchar2, valueformat.TypeNameNChar, valueformat.TypeNameTimestamp, valueformat.TypeNameBlob, valueformat.TypeNameClob, valueformat.TypeNameNClob} {
		for _, elements := range [][]any{nil, {nil}} {
			t.Run(typ+"/"+strings.Repeat("null", len(elements)), func(t *testing.T) {
				ctx, op := parameterDictionaryOperation(t, typ)
				var resolver catalog.Resolver
				p, err := EncodeValue(ctx, op, &resolver, types.Array{Schema: "S", Type: "TA", Elements: elements}, 4096)
				if !errors.Is(err, dberrors.ErrUnsupported) || p.Type != 0 || p.Data != nil {
					t.Fatal(p, err)
				}
				if err = op.BeginResponse(); err != nil {
					t.Fatal(err)
				}
				_, err = EncodeValue(ctx, op, &resolver, types.Array{ElementType: typ, Elements: elements}, 4096)
				op.EndResponse()
				if !errors.Is(err, dberrors.ErrUnsupported) {
					t.Fatal("unsupported built-in element attempted a descriptor query", err)
				}
			})
		}
	}
}
