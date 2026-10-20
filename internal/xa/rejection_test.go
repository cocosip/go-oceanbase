package xa

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// The public error-code test retains pool and error assertions; branch state belongs here.
func TestXAMySQLRejectionBranchState(t *testing.T) {
	for _, code := range []uint16{1397, 1398, 1399, 1400, 1401, 1402, 1440, 1613, 1614, 1064} {
		for _, framing := range []string{"standard", "compression", "ob20"} {
			for _, commit := range []bool{false, true} {
				t.Run(fmt.Sprintf("%d/%s/commit=%v", code, framing, commit), func(t *testing.T) {
					client, server := net.Pipe()
					defer func() { _ = client.Close(); _ = server.Close() }()
					if err := server.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
						t.Fatal(err)
					}
					packets, peer := wire.NewPacketIO(client, 4096), wire.NewPacketIO(server, 4096)
					for _, p := range []*wire.PacketIO{packets, peer} {
						var err error
						if framing == "compression" {
							err = p.EnableCompression()
						}
						if framing == "ob20" {
							err = p.EnableOB20(wire.OB20Options{ConnectionID: 42})
						}
						if err != nil {
							t.Fatal(err)
						}
					}
					done := make(chan struct{})
					go func() {
						defer close(done)
						ctx := context.Background()
						query := "XA START X'67',X'',0x1"
						if commit {
							for _, q := range []string{query, "XA END X'67',X'',0x1"} {
								if !testpeer.ServeOK(ctx, t, peer, []byte("\x03"+q), 2) {
									return
								}
							}
							query = "XA COMMIT X'67',X'',0x1 ONE PHASE"
						}
						packet := binary.LittleEndian.AppendUint16([]byte{255}, code)
						packet = append(packet, "#HY000XA server rejection"...)
						if !testpeer.ReadQuery(ctx, t, peer, query) {
							return
						}
						_ = peer.WriteMessage(ctx, packet)
					}()
					op := testOperation(t, config.Config{Limits: config.Limits{MaxMessageBytes: 4096, MaxResultItems: 16}}, &session.Negotiated{Raw: client, Conn: client, Packets: packets, Capabilities: wire.ClientProtocol41, Mode: config.ModeMySQL, OK: wire.OK{Status: 2}})
					m := &Manager{}
					x := XID{FormatID: 1, GlobalID: []byte("g")}
					r := &Request{Op: Start, XID: x}
					if commit {
						for _, kind := range []Operation{Start, End} {
							r.Op, r.Flags = kind, Success
							if kind == Start {
								r.Flags = NoFlags
							}
							if err := m.Validate(op, r, false); err != nil {
								t.Fatal(err)
							}
							if err := m.Execute(context.Background(), op, &catalog.Resolver{}, r); err != nil {
								t.Fatal(err)
							}
						}
						r.Op, r.OnePhase = Commit, true
					}
					if err := m.Validate(op, r, false); err != nil {
						t.Fatal(err)
					}
					if err := m.Execute(context.Background(), op, &catalog.Resolver{}, r); err == nil {
						t.Fatal("server rejection accepted")
					}
					b, known := m.branches[xidKey(x)]
					if m.active != "" || known != commit || commit && b.stage != ended {
						t.Errorf("server rejection changed local branch state: active=%q known=%v branch=%+v", m.active, known, b)
					}
					if op.Closed() {
						t.Fatal("complete rejection discarded session")
					}
					<-done
				})
			}
		}
	}
}
