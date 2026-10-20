package streaminput

import (
	"context"
	"encoding/hex"
	"io"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// Piece remains valid until the next call to UploadPlan.Next.
type Piece struct {
	Data  []byte
	Kind  byte
	Final bool
}

// UploadPlan converts one input chunk at a time, leaving fanout and ACKs to its caller.
type UploadPlan struct {
	source                   *BoundedReader
	value                    Source
	mode                     config.Mode
	charset                  string
	limit, encoded           int64
	size, pending, tailStart int
	buf                      []byte
	first, done              bool
}

// NewUploadPlan neither claims the input nor reads it. Its caller performs the
// existing parameter validation and claim before requesting the first piece.
func NewUploadPlan(ctx context.Context, value Source, mode config.Mode, charsetName string, maxLOBBytes, maxMessageBytes int64) *UploadPlan {
	limit := value.MaxBytes
	if limit == 0 {
		limit = maxLOBBytes
	}
	size := int(ChunkSize(mode, value.Text, maxMessageBytes))
	if charsetName == "" {
		charsetName = "utf8mb4"
	}
	return &UploadPlan{source: NewBoundedReader(ctx, value.Reader, value.Length, limit), value: value, mode: mode, charset: charsetName, limit: limit, size: size, buf: make([]byte, size+utf8.UTFMax), first: true}
}

// Next returns one bounded converted piece without claiming the source.
func (p *UploadPlan) Next() (Piece, error) {
	if p.done {
		return Piece{}, io.EOF
	}
	if p.pending > 0 {
		copy(p.buf, p.buf[p.tailStart:p.tailStart+p.pending])
	}
	n, err := ReadChunk(p.source, p.buf[p.pending:p.size])
	n += p.pending
	final := err == io.EOF
	if err != nil && !final {
		return Piece{}, err
	}
	if final && p.value.Length >= 0 && p.source.ReadCount() != p.value.Length {
		return Piece{}, io.ErrUnexpectedEOF
	}
	data := p.buf[:n]
	end := n
	if p.value.Text {
		end, err = TextBoundary(data, final)
		if err != nil {
			return Piece{}, err
		}
		data, err = charset.Encode(p.charset, string(data[:end]))
		if err != nil {
			return Piece{}, err
		}
	}
	if int64(len(data)) > p.limit-p.encoded {
		return Piece{}, wire.ErrMessageTooLarge
	}
	p.encoded += int64(len(data))
	if p.mode == config.ModeOracle && !p.value.Text {
		data = []byte(hex.EncodeToString(data))
	}
	kind := byte(2)
	if p.first {
		kind = 1
	}
	if final {
		kind = 3
	}
	p.done = final
	// Preserve the current piece until Next is called, including binary slices.
	p.pending = n - end
	p.tailStart = end
	p.first = false
	return Piece{Data: data, Kind: kind, Final: final}, nil
}

// ChunkSize computes a mode-specific bounded upload chunk size.
func ChunkSize(mode config.Mode, text bool, maxMessageBytes int64) int64 {
	size := min(int64(65536), maxMessageBytes-7)
	if mode == config.ModeOracle {
		size = (min(int64(65536), maxMessageBytes) - 17) / 2
	}
	if text {
		size /= 4
	}
	return size
}

// TextBoundary retains an incomplete UTF-8 suffix until the next source chunk.
func TextBoundary(data []byte, final bool) (int, error) {
	for i := 0; i < len(data); {
		r, n := utf8.DecodeRune(data[i:])
		if r == utf8.RuneError && n == 1 {
			if !final && !utf8.FullRune(data[i:]) {
				return i, nil
			}
			return 0, charset.ErrEncoding
		}
		i += n
	}
	return len(data), nil
}
