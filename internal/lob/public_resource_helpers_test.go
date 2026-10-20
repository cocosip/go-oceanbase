package lob_test

import (
	bytes "bytes"
	context "context"
	testing "testing"
	_ "time/tzdata"

	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
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

func serveTableBlobMutations(ctx context.Context, t *testing.T, p *wire.PacketIO) bool {
	t.Helper()
	// Sparse writes and extension attempts still lock and confirm the actual length.
	for range 2 {
		if !publictest.ServeTableRows(ctx, t, p, 3, []string{publictest.TestOctetLength, publictest.TestForUpdate}, []byte{8}, []any{"3"}) {
			return false
		}
	}
	for _, size := range []string{"3", "3", "4", "4"} {
		if !publictest.ServeTableRows(ctx, t, p, 3, []string{publictest.TestOctetLength, publictest.TestForUpdate}, []byte{8}, []any{size}) {
			return false
		}
		if size == "3" {
			command := publictest.ReadTableCommand(ctx, t, p)
			if !bytes.Contains(command, []byte("CONCAT(LEFT(`b`,")) {
				t.Errorf("unexpected mutation %q", command)
				return false
			}
			if !testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, 3, 0, 0, 0}) {
				return false
			}
		}
		if size == "4" {
			break
		}
	}
	if !publictest.ServeTableRows(ctx, t, p, 3, []string{publictest.TestOctetLength, publictest.TestForUpdate}, []byte{8}, []any{"4"}) {
		return false
	}
	command := publictest.ReadTableCommand(ctx, t, p)
	if !bytes.Contains(command, []byte("LEFT(`b`, 2 )")) {
		t.Errorf("unexpected trim %q", command)
		return false
	}
	if !testpeer.SendPackets(ctx, t, p, []byte{0, 1, 0, 3, 0, 0, 0}) {
		return false
	}
	return true
}
