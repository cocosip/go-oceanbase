package streaminput_test

import (
	"bytes"
	"context"
	"errors"
	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"io"
	"strings"
	"testing"
)

const localInputName = "input"
const localImportSQL = "LOAD DATA LOCAL INFILE 'input' INTO TABLE t"

func TestLocalInfileValidation(t *testing.T) {
	for _, input := range []*oceanbase.LocalInfile{
		nil, {}, {Name: localInputName}, {Reader: strings.NewReader("x")},
		{Name: "a\x00b", Reader: strings.NewReader("x")}, {Name: "\xff", Reader: strings.NewReader("x")},
		{Name: strings.Repeat("x", 4097), Reader: strings.NewReader("x")},
		{Name: localInputName, Reader: (*bytes.Reader)(nil)},
		{Name: localInputName, Reader: strings.NewReader("x"), MaxBytes: -1},
		{Name: localInputName, Reader: strings.NewReader("x"), Timeout: -1},
		{Name: localInputName, Reader: strings.NewReader("x"), Open: func(context.Context, string) (io.ReadCloser, error) {
			t.Fatal("invalid source opened")
			return nil, nil
		}},
	} {
		if _, err := oceanbase.Import(context.Background(), nil, localImportSQL, input); !errors.Is(err, oceanbase.ErrLocalInfile) {
			t.Fatal("invalid input accepted", err)
		}
	}
	db := publictest.QueryDB(t, func(_ context.Context, p *wire.PacketIO) { p.ResetSequence(); testpeer.ServeQuit(t, p) })
	defer func() { _ = db.Close() }()
	if _, err := oceanbase.Import(context.Background(), db, localImportSQL, &oceanbase.LocalInfile{Name: localInputName, Reader: strings.NewReader("x")}); !errors.Is(err, oceanbase.ErrUnsupported) {
		t.Fatal("unnegotiated capability", err)
	}
}
