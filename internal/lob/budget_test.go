package lob

import (
	"context"
	"errors"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestLOBChunkBudget(t *testing.T) {
	for _, limit := range []int64{512, 64} {
		physical := session.New(config.Config{Limits: config.Limits{MaxMessageBytes: limit}}, &session.Negotiated{})
		op, err := physical.Acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		native := &Native{raw: make([]byte, 36), column: wire.Column{Type: 210}}
		chunk, err := (&nativeOperation{op}).lobChunkSize(native, WriteSQL)
		op.Release()
		if limit == 64 {
			if !errors.Is(err, wire.ErrMessageTooLarge) {
				t.Fatal(err)
			}
			continue
		}
		if err != nil || chunk <= 0 {
			t.Fatal(chunk, err)
		}
		params := []wire.Parameter{native.parameter(), lobInteger(int64(chunk)), lobInteger(1), {Type: 203, Data: make([]byte, chunk), LengthEncoded: true}}
		if _, err = wire.EncodePrepareExecute(0, []byte(WriteSQL), params, limit); err != nil {
			t.Fatal("chunk exceeds command budget", err)
		}
	}
}
