package execution_test

import (
	context "context"
	binary "encoding/binary"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func openEpochLOB(ctx context.Context, t *testing.T, session oceanbase.Session, typ byte, strategy string) error {
	t.Helper()
	query, args := publictest.LobOpenEpochQuery, []any{1}
	if strategy == publictest.LobOpenText {
		query, args = publictest.LobOpenEpochLiteral, nil
	}
	if typ == 210 {
		lob, err := oceanbase.OpenBlob(ctx, session, query, args...)
		if lob != nil {
			_ = lob.Close()
			t.Fatal("failed opening published a BLOB")
		}
		return err
	}
	lob, err := oceanbase.OpenClob(ctx, session, query, args...)
	if lob != nil {
		_ = lob.Close()
		t.Fatal("failed opening published a CLOB")
	}
	return err
}

func lobOpenEpochConfig(strategy string) config.Config {
	cfg := config.Config{Mode: config.ModeOracle, PrepareMode: config.PrepareServer, Limits: config.Limits{CleanupTimeout: 200 * time.Millisecond}}
	if strategy == publictest.LobOpenClient {
		cfg.PrepareMode = config.PrepareClient
	}
	cfg.Execution.PrepareExecute = strategy == publictest.LobOpenCombined
	return cfg
}

func serveEpochLOBCommand(ctx context.Context, t *testing.T, p *wire.PacketIO, strategy string, prepareStatus byte) bool {
	t.Helper()
	switch strategy {
	case publictest.LobOpenText, publictest.LobOpenClient:
		return testpeer.ReadQuery(ctx, t, p, publictest.LobOpenEpochLiteral)
	case publictest.LobOpenCombined:
		return testpeer.ReadCommand(ctx, t, p, publictest.CombinedQueryPacket(0, publictest.LobOpenEpochQuery, 1, false, false))
	default:
		if !publictest.ServePrepareStatus(ctx, t, p, publictest.LobOpenEpochQuery, 95, 1, nil, prepareStatus) {
			return false
		}
		if prepareStatus == 4 {
			return true // The caller expects statement cleanup, without an execute command.
		}
		return testpeer.ReadCommand(ctx, t, p, binary.LittleEndian.AppendUint64(append(publictest.ExecuteHeader(95), 0, 1, 8, 0), 1))
	}
}
