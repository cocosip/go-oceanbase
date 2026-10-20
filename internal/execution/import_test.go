package execution

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/streaminput"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const importQuery = "LOAD DATA LOCAL INFILE 'fixture' INTO TABLE t"

type infileReader struct {
	reader io.Reader
	closed int
	err    error
}

func (r *infileReader) Read(p []byte) (int, error) { return r.reader.Read(p) }
func (r *infileReader) Close() error               { r.closed++; return r.err }

func readUpload(ctx context.Context, t *testing.T, p *wire.PacketIO, want string) bool {
	t.Helper()
	data, err := p.ReadMessage(ctx)
	if err != nil || string(data) != want {
		t.Error("uploaded data", string(data), want, err)
		return false
	}
	return true
}

func TestImportUploadOwnership(t *testing.T) {
	for _, owned := range []bool{false, true} {
		t.Run(map[bool]string{false: "caller reader", true: "callback reader"}[owned], func(t *testing.T) {
			source := &infileReader{reader: strings.NewReader("a,b\n")}
			ctx, engine := rowsProtocolEngine(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ReadCommand(ctx, t, p, append([]byte{3}, importQuery...)) || !testpeer.SendPackets(ctx, t, p, append([]byte{251}, "fixture"...)) || !readUpload(ctx, t, p, "a,b\n") || !readUpload(ctx, t, p, "") {
					return
				}
				if owned && source.closed != 1 {
					t.Error("owned reader not closed before terminal packet", source.closed)
				}
				if testpeer.SendPackets(ctx, t, p, []byte{0, 1, 0, 2, 0, 0, 0}) {
					testpeer.ServeOK(ctx, t, p, []byte{14}, 2)
				}
			})
			var used atomic.Bool
			input := &streaminput.Infile{Name: "fixture", Reader: source, State: &used}
			opened := 0
			if owned {
				input.Reader = nil
				input.Open = func(_ context.Context, name string) (io.ReadCloser, error) {
					opened++
					if name != "fixture" {
						t.Error(name)
					}
					return source, nil
				}
			}
			if err := input.Validate(); err != nil || !input.Claim() {
				t.Fatal(err)
			}
			result, err := engine.Import(ctx, importQuery, input)
			if err != nil || result.Affected != 1 || !used.Load() || source.closed != map[bool]int{false: 0, true: 1}[owned] || opened != map[bool]int{false: 0, true: 1}[owned] {
				t.Fatal(result, err, source.closed, opened)
			}
			if err = engine.Ping(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestImportCallbackErrorClosesReaderAndPreventsReplay(t *testing.T) {
	ctx, engine := rowsProtocolEngine(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadCommand(ctx, t, p, append([]byte{3}, importQuery...)) || !testpeer.SendPackets(ctx, t, p, append([]byte{251}, "fixture"...)) {
			return
		}
		if data, err := p.ReadMessage(ctx); err == nil {
			t.Error("failed callback sent input", data)
		}
	})
	source := &infileReader{reader: strings.NewReader("unused")}
	input := &streaminput.Infile{Open: func(context.Context, string) (io.ReadCloser, error) { return source, driver.ErrBadConn }}
	if _, err := engine.Import(ctx, importQuery, input); !errors.Is(err, dberrors.ErrLocalInfile) || errors.Is(err, driver.ErrBadConn) || engine.Valid() || source.closed != 1 {
		t.Fatal(err, engine.Valid(), source.closed)
	}
}

func TestImportRefusesRepeatedRequest(t *testing.T) {
	ctx, engine := rowsProtocolEngine(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		request := append([]byte{251}, "fixture"...)
		if !testpeer.ReadCommand(ctx, t, p, append([]byte{3}, importQuery...)) || !testpeer.SendPackets(ctx, t, p, request) || !readUpload(ctx, t, p, "a") || !readUpload(ctx, t, p, "") || !testpeer.SendPackets(ctx, t, p, request) {
			return
		}
		if data, err := p.ReadMessage(ctx); err == nil {
			t.Error("second server request reused authorization", data)
		}
	})
	opened := 0
	input := &streaminput.Infile{Open: func(context.Context, string) (io.ReadCloser, error) {
		opened++
		return io.NopCloser(strings.NewReader("a")), nil
	}}
	if _, err := engine.Import(ctx, importQuery, input); !errors.Is(err, dberrors.ErrLocalInfile) || opened != 1 || engine.Valid() {
		t.Fatal(err, opened, engine.Valid())
	}
}

func TestImportValidatesBeforeInput(t *testing.T) {
	ctx, engine := rowsProtocolEngine(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		testpeer.ServeOK(ctx, t, p, []byte{14}, 2)
	})
	opened := false
	input := &streaminput.Infile{Open: func(context.Context, string) (io.ReadCloser, error) { opened = true; return nil, nil }}
	if _, err := engine.Import(ctx, "SELECT 1", input); err == nil || opened || !engine.Valid() {
		t.Fatal(err, opened, engine.Valid())
	}
	if err := engine.Ping(ctx); err != nil {
		t.Fatal("invalid import sent SQL or retained operation", err)
	}
}
