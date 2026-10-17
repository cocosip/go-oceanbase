package wire

import (
	"context"
	"testing"
)

func FuzzFrames(f *testing.F) {
	f.Add(false, compressedFixture(f, []byte{1, 0, 0, 0, 'x'}, 0, true))
	f.Add(false, []byte{5, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 'x'})
	f.Add(true, ob20Fixture([]byte{1, 0, 0, 0, 'x'}, 0, 2))
	f.Add(true, []byte{})
	f.Fuzz(func(t *testing.T, ob bool, data []byte) {
		if len(data) > 65536 {
			t.Skip()
		}
		p := NewPacketIO(newBufferConn(data), 4096)
		_, _ = DecodeSessionVariables(data)
		if ob {
			_ = p.EnableOB20(OB20Options{ConnectionID: 42})
		} else {
			_ = p.EnableCompression()
		}
		p.ResetSequence()
		for range 4 {
			got, err := p.ReadMessage(context.Background())
			if err != nil {
				return
			}
			if len(got) > 4096 {
				t.Fatal("logical message exceeded budget")
			}
		}
	})
}
