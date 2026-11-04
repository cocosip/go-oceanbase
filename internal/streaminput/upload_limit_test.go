package streaminput

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
)

func TestUploadPlanPieceLimit(t *testing.T) {
	const maxMessage = 64
	size := int(ChunkSize(config.ModeMySQL, true, maxMessage))
	if size <= 0 {
		t.Fatalf("unexpected chunk size %d", size)
	}
	for _, tc := range []struct {
		name    string
		length  int
		wantErr error
	}{
		{"at-limit", MaxPieces * size, io.EOF},
		{"over-limit", MaxPieces*size + 1, ErrTooManyPieces},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := Source{
				Reader: strings.NewReader(strings.Repeat("a", tc.length)),
				Length: -1,
				Text:   true,
				State:  &atomic.Bool{},
			}
			plan := NewUploadPlan(context.Background(), source, config.ModeMySQL, "utf8mb4", int64(tc.length)+1, maxMessage)
			pieces := 0
			var err error
			for {
				var piece Piece
				piece, err = plan.Next()
				if err != nil {
					break
				}
				if len(piece.Data) > 0 {
					pieces++
				}
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err=%v want=%v", err, tc.wantErr)
			}
			if pieces != MaxPieces {
				t.Fatalf("data pieces=%d want=%d", pieces, MaxPieces)
			}
		})
	}
}
