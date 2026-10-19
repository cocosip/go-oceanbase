package oceanbase

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const (
	sessionReadSchema      = "schema"
	sessionReadCatalog     = "catalog"
	sessionReadInfo        = "info"
	sessionReadVariable    = "variable"
	sessionReadDatabaseSQL = "SELECT DATABASE()"
	sessionReadTailError   = "error-after-row"
	sessionReadExtraRow    = "extra-row"
)

func TestSessionReadResponseBoundary(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, framing := range []string{"standard", testCompression, testOB20} {
			for _, path := range []string{"error-before-row", sessionReadTailError, "no-row", sessionReadExtraRow} {
				t.Run(fmt.Sprintf("%v/%s/%s", mode, framing, path), func(t *testing.T) {
					for _, helper := range []string{sessionReadSchema, sessionReadCatalog, sessionReadInfo, sessionReadVariable} {
						if helper == sessionReadCatalog && mode == ModeOracle {
							continue
						}
						t.Run(helper, func(t *testing.T) { testSessionReadResponse(t, mode, framing, path, helper) })
					}
				})
			}
		}
	}
}

func sessionReadFixture(mode Mode, helper string) (string, int) {
	query, columns := sessionReadDatabaseSQL, 1
	switch helper {
	case sessionReadSchema:
		if mode == ModeOracle {
			query = "SELECT SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA')"
		}
	case sessionReadInfo:
		query, columns = "SELECT @ApplicationName, @ClientUser, @ClientHostname", 3
	case sessionReadVariable:
		query = testReadSessionVariable
	}
	if mode == ModeOracle {
		query += sessionReadDualSuffix
	}
	return query, columns
}

func readSessionFixture(ctx context.Context, pinned *sql.Conn, helper string) (any, error) {
	switch helper {
	case sessionReadSchema:
		return CurrentSchema(ctx, pinned)
	case sessionReadCatalog:
		return CurrentCatalog(ctx, pinned)
	case sessionReadInfo:
		return GetClientInfo(ctx, pinned)
	default:
		return SessionVariable(ctx, pinned, "wait_timeout")
	}
}

func serveSessionReadFailure(ctx context.Context, t *testing.T, p *wire.PacketIO, query string, columns int, status byte, path string) {
	t.Helper()
	if !readQuery(ctx, t, p, query) || !sendPackets(ctx, t, p, []byte{byte(columns)}) {
		return
	}
	for range columns {
		if !sendPackets(ctx, t, p, textColumn("v", 253, 45, 0)) {
			return
		}
	}
	if !sendPackets(ctx, t, p, []byte{254, 0, 0, status, 0}) {
		return
	}
	row := make([]byte, 0, columns*2)
	for range columns {
		row = append(row, 1, 'v')
	}
	if path == sessionReadTailError || path == sessionReadExtraRow {
		if !sendPackets(ctx, t, p, row) {
			return
		}
	}
	switch path {
	case "error-before-row", sessionReadTailError:
		packet := []byte{255, 21, 4, '#', '4', '2', '0', '0', '0'}
		if !sendPackets(ctx, t, p, append(packet, "session query denied"...)) || !serveOK(ctx, t, p, []byte{14}, status) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	default:
		packet := []byte{254, 0, 0, status, 0}
		if path == sessionReadExtraRow {
			packet = row
		}
		if !sendPackets(ctx, t, p, packet) {
			return
		}
		_, err := p.ReadMessage(ctx)
		if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
			t.Error("malformed scalar response did not close the connection", err)
		}
	}
}

func testSessionReadResponse(t *testing.T, mode Mode, framing, path, helper string) {
	t.Helper()
	query, columns := sessionReadFixture(mode, helper)
	status := byte(2)
	if mode == ModeOracle {
		status = 6
	}
	db := localInfileDB(t, mode, false, func(ctx context.Context, p *wire.PacketIO) {
		serveSessionReadFailure(ctx, t, p, query, columns, status, path)
	}, func(cfg *Config) {
		switch framing {
		case testCompression:
			cfg.Protocol.Compression = FeatureRequired
		case testOB20:
			cfg.Protocol.OB20 = FeatureRequired
		}
	})
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	value, err := readSessionFixture(ctx, pinned, helper)
	if !reflect.ValueOf(value).IsZero() {
		t.Error("failed scalar read published a partial value", value)
	}
	if path == "no-row" || path == sessionReadExtraRow {
		if !errors.Is(err, wire.ErrMalformed) {
			t.Fatal("invalid scalar shape was accepted", err)
		}
		if err = pinned.PingContext(ctx); err == nil {
			t.Fatal("malformed scalar response remained usable")
		}
		return
	}
	var server *Error
	if !errors.As(err, &server) || server.Code != 1045 || server.SQLState != "42000" || server.Message != "session query denied" {
		t.Fatal("complete server error lost its cause", err)
	}
	if err = pinned.PingContext(ctx); err != nil {
		t.Fatal("complete scalar error discarded original session", err)
	}
}
