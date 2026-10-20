package connect_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/authentication"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestProxyUserChangeUserPreflight(t *testing.T) {
	for _, attributes := range []bool{false, true} {
		t.Run(fmt.Sprint(attributes), func(t *testing.T) {
			created, err := oceanbase.NewConnector(config.Config{User: testUser, Mode: config.ModeOracle, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				greeting := testpeer.AuthGreeting(authentication.Native, false)
				if attributes {
					greeting = testpeer.ProtocolGreeting(false)
				}
				if !testpeer.SendPackets(ctx, t, p, greeting) || !testpeer.ReadAuthResponse(ctx, t, p) || !testpeer.SendPackets(ctx, t, p, commandPacket(0, 0, 6)) || !testpeer.ServeBaseline(ctx, t, p, 6) {
					return
				}
				ctx = context.Background()
				if !testpeer.ServeOK(ctx, t, p, []byte{14}, 6) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})})
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(created)
			defer func() { _ = db.Close() }()
			ctx := context.Background()
			pinned, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = pinned.Close() }()
			proxy := testProxyUser
			if attributes {
				proxy = "invalid\x00proxy"
			}
			err = oceanbase.ChangeUser(ctx, pinned, config.Credentials{User: changeUserName, ProxyUser: proxy}, "")
			if err == nil || !attributes && !errors.Is(err, dberrors.ErrUnsupported) {
				t.Fatal("proxy preflight failure not rejected", err)
			}
			if err = pinned.PingContext(ctx); err != nil {
				t.Fatal("proxy preflight changed original identity", err)
			}
		})
	}
}

func TestProxyUserCancelIdentity(t *testing.T) {
	for _, auxProxy := range []string{"", "other_client", testProxyUser} {
		t.Run(fmt.Sprintf("aux=%s", auxProxy), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			started, finish := make(chan struct{}), make(chan struct{})
			main, err := oceanbase.NewConnector(config.Config{User: testUser, ProxyUser: testProxyUser, DialContext: testpeer.PeerDial(t, func(_ context.Context, p *wire.PacketIO) {
				if !serveProxyIdentity(ctx, t, p, 42) || !testpeer.ServeBaseline(ctx, t, p, 6) || !testpeer.ReadQuery(ctx, t, p, "SELECT waiting") {
					return
				}
				close(started)
				select {
				case <-finish:
				case <-ctx.Done():
					return
				}
				if !testpeer.SendPackets(ctx, t, p, commandPacket(0, 0, 6)) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 6) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})})
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(main)
			defer func() { _ = db.Close() }()
			pinned, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = pinned.Close() }()
			target, err := oceanbase.QueryTarget(ctx, pinned)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, err := pinned.ExecContext(ctx, "SELECT waiting"); done <- err }()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			aux, err := oceanbase.NewConnector(config.Config{User: testUser, Credentials: func(context.Context) (config.Credentials, error) {
				return config.Credentials{User: testUser, ProxyUser: auxProxy}, nil
			}, DialContext: testpeer.PeerDial(t, func(_ context.Context, p *wire.PacketIO) {
				defer close(finish)
				if !serveProxyIdentity(ctx, t, p, 43) || !testpeer.ServeBaseline(ctx, t, p, 6) {
					return
				}
				if auxProxy == testProxyUser && !testpeer.ServeOK(ctx, t, p, []byte("\x03KILL QUERY 42"), 6) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})})
			if err != nil {
				t.Fatal(err)
			}
			err = oceanbase.CancelQuery(ctx, aux, target)
			if auxProxy == testProxyUser && err != nil || auxProxy != testProxyUser && !errors.Is(err, oceanbase.ErrResource) {
				t.Fatal("cancel failed to distinguish effective proxy identities", err)
			}
			if err = <-done; err != nil {
				t.Fatal(err)
			}
			if err = pinned.PingContext(ctx); err != nil {
				t.Fatal("identity rejection discarded original session", err)
			}
		})
	}
}
