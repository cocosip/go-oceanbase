package wire

import (
	"context"
	"testing"
)

func FuzzPacket(f *testing.F) {
	for _, seed := range [][]byte{{}, {0, 0, 0, 0}, {1, 0, 0, 0, 0x42}, {255, 255, 255, 0}, {1, 0, 0, 7, 0}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 8192 {
			t.Skip()
		}
		p := NewPacketIO(newBufferConn(data), 4096)
		got, err := p.ReadMessage(context.Background())
		if err == nil && len(got) > 4096 {
			t.Fatal("message exceeded configured limit")
		}
	})
}
