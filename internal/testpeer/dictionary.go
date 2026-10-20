package testpeer

import (
	"context"
	"fmt"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// DictionaryOperation opens a raw owner fixture without public facade or handshake dependencies.
func DictionaryOperation(t *testing.T, cfg config.Config, serve func(context.Context, *wire.PacketIO)) *session.Operation {
	t.Helper()
	cfg.User, cfg.Mode = User, config.ModeOracle
	normalized, err := config.Normalize(cfg)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := PeerDial(t, serve)(t.Context(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	physical := session.New(normalized, &session.Negotiated{Raw: raw, Conn: raw, Packets: wire.NewPacketIO(raw, normalized.Limits.MaxMessageBytes), Mode: config.ModeOracle, Capabilities: wire.ClientProtocol41, OBVersion: "4.3.5.1"})
	op, err := physical.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { op.Release(); physical.Discard() })
	return op
}

// ServeComplexType loads the same immutable descriptor previously seeded by owner tests.
func ServeComplexType(ctx context.Context, t *testing.T, p *wire.PacketIO, names, types []string) bool {
	t.Helper()
	rows := [][][]string{{{"", "S", "T", "OBJECT", fmt.Sprint(len(names)), "1", "1"}}, {}}
	for i, name := range names {
		rows[1] = append(rows[1], []string{"", "S", "T", name, fmt.Sprint(i + 1), types[i], "", "", "", ""})
	}
	for _, result := range rows {
		p.ResetSequence()
		command, err := p.ReadMessage(ctx)
		if err != nil || len(command) == 0 || command[0] != 3 {
			t.Error("missing descriptor query", err)
			return false
		}
		columns := 7
		if len(result) != 0 {
			columns = len(result[0])
		}
		packets := [][]byte{{byte(columns)}}
		for range columns {
			packets = append(packets, TextColumn("field", 253, 45, 0))
		}
		packets = append(packets, []byte{254, 0, 0, 6, 0})
		for _, row := range result {
			var data []byte
			for _, value := range row {
				data = wire.AppendComplexLenBytes(data, []byte(value))
			}
			packets = append(packets, data)
		}
		packets = append(packets, []byte{254, 0, 0, 6, 0})
		if !SendPackets(ctx, t, p, packets...) {
			return false
		}
	}
	return true
}
