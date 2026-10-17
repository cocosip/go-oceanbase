package wire

import (
	"bytes"
	"context"
	"testing"
)

func TestContinuationSequenceWrap(t *testing.T) {
	for _, framing := range []string{"standard", "compress", "ob20"} {
		t.Run(framing, func(t *testing.T) {
			transport := newBufferConn(nil)
			p := NewPacketIO(transport, 1024)
			enable := func(p *PacketIO) {
				switch framing {
				case "compress":
					if err := p.EnableCompression(); err != nil {
						t.Fatal(err)
					}
				case "ob20":
					if err := p.EnableOB20(OB20Options{ConnectionID: 42, Checksum: true}); err != nil {
						t.Fatal(err)
					}
				}
				p.ResetSequence()
			}
			enable(p)
			p.EnableDiagnostics(4, 4)
			ctx := context.Background()
			if err := p.WriteMessage(ctx, []byte{3, 'x'}); err != nil {
				t.Fatal(err)
			}
			for range 260 {
				if err := p.WriteContinuation(ctx, []byte{3, 's'}); err != nil {
					t.Fatal(err)
				}
			}
			if err := p.WriteContinuation(ctx, nil); err != nil {
				t.Fatal(err)
			}
			records, _ := p.TakeDiagnostics()
			if len(records) != 1 || records[0].SQLBytes != 1 || records[0].WriteBytes != int64(transport.out.Len()) {
				t.Fatal("continuation started another command", records)
			}
			peer := NewPacketIO(newBufferConn(transport.out.Bytes()), 1024)
			enable(peer)
			for i := range 262 {
				b, err := peer.ReadMessage(ctx)
				want := []byte{3, 's'}
				if i == 0 {
					want = []byte{3, 'x'}
				}
				if i == 261 {
					want = nil
				}
				if err != nil || !bytes.Equal(b, want) {
					t.Fatal("continued frame/sequence", i, b, err)
				}
			}
		})
	}
}
