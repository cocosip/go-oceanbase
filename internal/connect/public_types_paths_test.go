package connect_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestTypeSessionInitialization(t *testing.T) {
	connector, err := oceanbase.NewConnector(config.Config{User: testUser, Session: config.SessionOptions{Location: time.FixedZone("business", 8*3600), NCharset: testCharsetUTF16, InitSQL: []string{"SET SESSION time_zone = '-03:00'"}}, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeConnect(ctx, t, p, 6) || !testpeer.ServeOK(ctx, t, p, []byte("\x03SET NAMES utf8mb4"), 6) || !testpeer.ServeOK(ctx, t, p, []byte("\x03SET autocommit=1"), 6) || !testpeer.ServeVersion(ctx, t, p, 6, false) || !testpeer.ServeOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+08:00'"), 6) || !testpeer.ServeOK(ctx, t, p, []byte("\x03SET NCHARACTER_SET_CONNECTION = 'utf16'"), 6) || !testpeer.ServeOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '-03:00'"), 6) {
			return
		}
		for _, query := range []string{"SET NAMES utf8mb4", "SET SESSION time_zone = '+08:00'", "SET NCHARACTER_SET_CONNECTION = 'utf16'"} {
			if !testpeer.ServeOK(ctx, t, p, append([]byte{3}, query...), 6) {
				return
			}
		}
		if !testpeer.ServeOK(context.Background(), t, p, []byte{14}, 6) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})})
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	if err = db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}
