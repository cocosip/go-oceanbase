package streaminput

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

type mapReader map[string]byte

func (mapReader) Read([]byte) (int, error) { return 0, io.EOF }

type sliceReader []byte

func (sliceReader) Read([]byte) (int, error) { return 0, io.EOF }

type chanReader chan byte

func (chanReader) Read([]byte) (int, error) { return 0, io.EOF }

func TestLongDataReaderBounds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reader io.Reader
		want   error
	}{
		{"idle", readerFunc(func([]byte) (int, error) { return 0, nil }), io.ErrNoProgress},
		{"negative", readerFunc(func([]byte) (int, error) { return -1, nil }), dberrors.ErrLongData},
		{"large", readerFunc(func(p []byte) (int, error) { return len(p) + 1, nil }), dberrors.ErrLongData},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewBoundedReader(context.Background(), tc.reader, -1, 100)
			if _, err := ReadChunk(s, make([]byte, 4)); !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
		})
	}
}

func TestLongDataNilReader(t *testing.T) {
	for _, reader := range []io.Reader{nil, (*bytes.Reader)(nil), readerFunc(nil), mapReader(nil), sliceReader(nil), chanReader(nil)} {
		t.Run(fmt.Sprintf("%T", reader), func(t *testing.T) {
			s := Source{Reader: reader, Length: -1, State: new(atomic.Bool)}
			if err := s.Validate(); !errors.Is(err, dberrors.ErrLongData) {
				t.Fatalf("nil Reader accepted: %v", err)
			}
		})
	}
}

func TestSourceStateAndBounds(t *testing.T) {
	state := new(atomic.Bool)
	s := Source{Reader: strings.NewReader("x"), Length: 1, State: state}
	copied := s
	if err := s.Validate(); err != nil || state.Load() {
		t.Fatal(err)
	}
	if !s.Claim() || copied.Claim() || !errors.Is(copied.Validate(), dberrors.ErrLongData) {
		t.Fatal("state was not shared")
	}
	for _, tc := range []struct {
		data          string
		length, limit int64
		want          error
	}{
		{"a", 0, 10, dberrors.ErrLongData}, {"a", 2, 10, io.ErrUnexpectedEOF},
		{"ab", -1, 1, wire.ErrMessageTooLarge}, {"a", 1, 1, nil},
	} {
		r := NewBoundedReader(context.Background(), strings.NewReader(tc.data), tc.length, tc.limit)
		_, err := ReadChunk(r, make([]byte, 4))
		if tc.want == nil {
			if err != io.EOF {
				t.Fatal(err)
			}
		} else if !errors.Is(err, tc.want) {
			t.Fatal(tc, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := NewBoundedReader(ctx, readerFunc(func(p []byte) (int, error) { cancel(); p[0] = 'x'; return 1, nil }), -1, 10)
	if n, err := r.Read(make([]byte, 4)); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatal(n, err)
	}
}

func TestUploadPlan(t *testing.T) {
	for _, tc := range []struct {
		mode  config.Mode
		text  bool
		input string
		want  []string
	}{
		{config.ModeMySQL, false, testChunkText, []string{testChunkText, ""}},
		{config.ModeOracle, false, testChunkText, []string{"61626364", ""}},
		{config.ModeMySQL, true, "a你🙂", []string{"a你", "🙂", ""}},
	} {
		s := Source{Reader: strings.NewReader(tc.input), Length: int64(len(tc.input)), Text: tc.text, State: new(atomic.Bool)}
		maxMessage := int64(11)
		if tc.text {
			maxMessage = 23
		}
		if tc.mode == config.ModeOracle {
			maxMessage = 25
		}
		plan := NewUploadPlan(context.Background(), s, tc.mode, "utf8mb4", 100, maxMessage)
		for i, want := range tc.want {
			piece, err := plan.Next()
			kind := byte(2)
			if i == 0 {
				kind = 1
			}
			if i == len(tc.want)-1 {
				kind = 3
			}
			if err != nil || string(piece.Data) != want || piece.Kind != kind || piece.Final != (kind == 3) {
				t.Fatal(i, piece, err)
			}
		}
		if _, err := plan.Next(); err != io.EOF {
			t.Fatal(err)
		}
	}
}

func TestUploadPlanPartialRuneAndBudgets(t *testing.T) {
	var calls int
	reader := strings.NewReader("a你🙂")
	s := Source{Reader: readerFunc(func(p []byte) (int, error) { calls++; return reader.Read(p) }), Length: 8, Text: true}
	p := NewUploadPlan(context.Background(), s, config.ModeMySQL, "utf8mb4", 100, 27)
	if calls != 0 {
		t.Fatal("planning read input")
	}
	first, err := p.Next()
	if err != nil || string(first.Data) != "a你" || first.Final {
		t.Fatal(first, err)
	}
	last, err := p.Next()
	if err != nil || string(last.Data) != "🙂" || !last.Final || last.Kind != 3 {
		t.Fatal(last, err)
	}
	if string(first.Data) != "a你" {
		t.Fatal("converted chunk changed")
	}
	for _, tc := range []struct {
		input, charset string
		sourceLimit    int64
		want           error
	}{
		{testChunkText, "utf8mb4", 3, wire.ErrMessageTooLarge},
		{"中", "utf16", 1, wire.ErrMessageTooLarge},
		{"a", "utf16", 1, wire.ErrMessageTooLarge},
		{"\xff", "utf8mb4", 10, charset.ErrEncoding},
	} {
		s := Source{Reader: strings.NewReader(tc.input), Length: -1, Text: true, MaxBytes: tc.sourceLimit}
		p := NewUploadPlan(context.Background(), s, config.ModeMySQL, tc.charset, 100, 65536)
		if _, err := p.Next(); !errors.Is(err, tc.want) {
			t.Fatal(tc, err)
		}
	}
}

func FuzzLongTextBoundary(f *testing.F) {
	for _, seed := range [][]byte{[]byte("a你🙂"), {0xe4, 0xbd}, {0xff}, {0xe4, 0x41}, {}} {
		f.Add(seed, true)
		f.Add(seed, false)
	}
	f.Fuzz(func(t *testing.T, data []byte, final bool) {
		if len(data) > 65536 {
			return
		}
		end, err := TextBoundary(data, final)
		valid := utf8.Valid(data)
		partial := -1
		if !final && !valid {
			for n := 1; n <= 3 && n <= len(data); n++ {
				i := len(data) - n
				if utf8.Valid(data[:i]) && !utf8.FullRune(data[i:]) {
					partial = i
					break
				}
			}
		}
		if valid {
			if err != nil || end != len(data) {
				t.Fatal("valid text rejected")
			}
			return
		}
		if partial >= 0 {
			if err != nil || end != partial {
				t.Fatal("incomplete rune mishandled")
			}
			return
		}
		if !errors.Is(err, charset.ErrEncoding) {
			t.Fatal("invalid text accepted")
		}
	})
}

const testChunkText = "abcd"
