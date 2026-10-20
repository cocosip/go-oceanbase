package catalog

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/metadata"
)

type dictionaryEntry struct {
	kind         metadata.MetadataKind
	schema, name string
	rows         [][]string
}

func dictionaryOperation(t *testing.T, limit int64, entries []dictionaryEntry) (context.Context, *session.Session, *session.Operation) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	client, peer := net.Pipe()
	t.Cleanup(cancel)
	t.Cleanup(func() { _ = client.Close(); _ = peer.Close() })
	cfg, err := config.Normalize(config.Config{User: routineApp, Addr: "127.0.0.1:2881", Mode: config.ModeOracle})
	if err != nil {
		t.Fatal(err)
	}
	if limit > 0 {
		cfg.Limits.MaxMessageBytes = limit
	}
	s := session.New(cfg, &session.Negotiated{Raw: client, Conn: client, Packets: wire.NewPacketIO(client, 0), Mode: config.ModeOracle, Capabilities: wire.ClientProtocol41})
	op, err := s.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { op.Release() })
	commands := make([][]byte, len(entries))
	for i, e := range entries {
		plan, err := Plan(config.ModeOracle, "4.3.5.0", metadata.MetadataFilter{Kind: e.kind, SchemaPattern: ExactDescriptorPattern(e.schema), NamePattern: ExactDescriptorPattern(e.name)})
		if err != nil {
			t.Fatal(err)
		}
		data, err := command.Interpolate(op, plan.SQL, plan.Args)
		if err != nil {
			t.Fatal(err)
		}
		commands[i] = append([]byte{3}, data...)
	}
	go func() {
		p := wire.NewPacketIO(testpeer.AuthPeerConn{Conn: peer}, 0)
		for i, e := range entries {
			p.ResetSequence()
			if !testpeer.ReadCommand(ctx, t, p, commands[i]) {
				return
			}
			cols := Columns(e.kind)
			packets := [][]byte{{byte(len(cols))}}
			for _, c := range cols {
				packets = append(packets, testpeer.TextColumn(c.Name, 253, 45, 0))
			}
			packets = append(packets, []byte{254, 0, 0, 2, 0})
			for _, row := range e.rows {
				var data []byte
				for _, v := range row {
					data = wire.AppendComplexLenBytes(data, []byte(v))
				}
				packets = append(packets, data)
			}
			packets = append(packets, []byte{254, 0, 0, 2, 0})
			if !testpeer.SendPackets(ctx, t, p, packets...) {
				return
			}
		}
	}()
	return ctx, s, op
}

func TestDescriptorSynonymAndBoundary(t *testing.T) {
	entries := []dictionaryEntry{
		{metadata.MetadataTypes, "S", descriptorAlias, nil},
		{metadata.MetadataSynonyms, "S", descriptorAlias, [][]string{{"", "S", descriptorAlias, "S", "T", ""}}},
		{metadata.MetadataTypes, "S", "T", [][]string{{"", "S", "T", "OBJECT", "1", "1", "1"}}},
		{metadata.MetadataTypeAttributes, "S", "T", [][]string{{"", "S", "T", "N", "1", "NUMBER", "SYS", "", "", ""}}},
	}
	ctx, _, op := dictionaryOperation(t, 1<<16, entries)
	var r Resolver
	key := valuecodec.Key{Mode: config.ModeOracle, Schema: "S", Name: descriptorAlias, Version: 9}
	if err := op.BeginResponse(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(ctx, op, key); !errors.Is(err, ErrResponseActive) {
		t.Fatal(err)
	}
	op.EndResponse()
	d, err := r.Resolve(ctx, op, key)
	if err != nil || d.Key.Name != "T" || d.Key.Version != 9 || len(d.Fields) != 1 || d.Fields[0].Name != "N" {
		t.Fatal(d, err)
	}
	got, err := r.Resolve(ctx, op, key)
	if err != nil || got != d {
		t.Fatal(got, err)
	}
	if err := descriptorBudgetError(&r, op.Config().Limits.MaxMessageBytes); err != nil {
		t.Fatal(err)
	}
}

func TestDescriptorBudget(t *testing.T) {
	fields := make([][]string, 40)
	for i := range fields {
		fields[i] = []string{"", "S", "T", fmt.Sprintf("F%d", i), fmt.Sprint(i + 1), "NUMBER", "SYS", "", "", ""}
	}
	ctx, s, op := dictionaryOperation(t, 10500, []dictionaryEntry{
		{metadata.MetadataTypes, "S", "T", [][]string{{"", "S", "T", "OBJECT", "40", "1", "1"}}},
		{metadata.MetadataTypeAttributes, "S", "T", fields},
	})
	var r Resolver
	_, err := r.Resolve(ctx, op, valuecodec.Key{Mode: config.ModeOracle, Schema: "S", Name: "T"})
	if !errors.Is(err, wire.ErrMessageTooLarge) || s.Closed() {
		t.Fatal(err, s.Closed(), r.bytes)
	}
	if err := descriptorBudgetError(&r, op.Config().Limits.MaxMessageBytes); err != nil {
		t.Fatal(err)
	}
}

func descriptorBudgetError(r *Resolver, limit int64) error {
	if r.bytes < 0 || r.bytes > limit {
		return fmt.Errorf("type descriptors exceed their byte budget")
	}
	return nil
}

func TestDescriptorResourceGuard(t *testing.T) {
	for _, size := range []int64{-1, 0, 1} {
		r := Resolver{bytes: size}
		if err := descriptorBudgetError(&r, 0); (err == nil) != (size == 0) {
			t.Fatal("descriptor guard missed an invalid byte budget", size, err)
		}
		r.Clear()
		if r.bytes != 0 || r.cache != nil {
			t.Fatal("descriptor clear retained a command budget")
		}
	}
}

const descriptorAlias = "ALIAS"
