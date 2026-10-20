package connect_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/authentication"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestCreateDatabaseHandshake(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, serverDB := range []bool{false, true} {
			t.Run(fmt.Sprintf("%t/%t", enabled, serverDB), func(t *testing.T) {
				_, err := runAuthTest(t, config.Config{User: testUser, Database: "private-target", CreateDatabaseIfMissing: enabled}, func(ctx context.Context, p *wire.PacketIO) {
					greeting := testpeer.AuthGreeting(authentication.Native, false)
					if !serverDB {
						i := bytes.IndexByte(greeting[1:], 0) + 2 + 4 + 8 + 1
						binary.LittleEndian.PutUint16(greeting[i:], binary.LittleEndian.Uint16(greeting[i:])&^uint16(wire.ClientConnectWithDB))
					}
					if !testpeer.SendPackets(ctx, t, p, greeting) {
						return
					}
					data, readErr := p.ReadMessage(ctx)
					if !enabled && !serverDB {
						if readErr == nil {
							t.Error("ordinary login ignored unavailable initial database capability")
						}
						return
					}
					if readErr != nil || len(data) < 4 {
						t.Error("missing authentication response", readErr)
						return
					}
					withDB := binary.LittleEndian.Uint32(data)&uint32(wire.ClientConnectWithDB) != 0
					if withDB == enabled || bytes.Contains(data, []byte("private-target")) == enabled {
						t.Error("creation policy did not defer initial database selection")
					}
					testpeer.SendPackets(ctx, t, p, testpeer.ProtocolOK(2, nil))
				})
				if !enabled && !serverDB {
					if !errors.Is(err, dberrors.ErrUnsupported) {
						t.Fatal("ordinary database capability failure hidden", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestCreateDatabaseInitialization(t *testing.T) {
	for _, framing := range []string{testStandard, testCompression, testOB20} {
		for _, encoding := range []struct{ charset, name, encoded string }{
			{testUTF8MB4, "db`x;--", "db`x;--"},
			{localFilenameCharset, "测试`x", "\xb2\xe2\xca\xd4`x"},
		} {
			t.Run(framing+"/"+encoding.charset, func(t *testing.T) {
				cfg := config.Config{User: testUser, Database: encoding.name, CreateDatabaseIfMissing: true, Session: config.SessionOptions{Charset: encoding.charset, InitSQL: []string{"SET @ready = 1"}}}
				switch framing {
				case testCompression:
					cfg.Protocol.Compression = config.FeatureRequired
				case testOB20:
					cfg.Protocol.OB20 = config.FeatureRequired
				}
				cfg.DialContext = testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
					names := "SET NAMES " + encoding.charset
					if !testpeer.ServeCreateDatabaseAuth(ctx, t, p, framing, 2) || !testpeer.ServeOK(ctx, t, p, []byte("\x03"+names), 2) || !testpeer.ServeOK(ctx, t, p, []byte("\x03SET autocommit=1"), 2) || !testpeer.ServeVersion(ctx, t, p, 2, false) || !testpeer.ServeOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+00:00'"), 2) {
						return
					}
					quoted := "`" + strings.ReplaceAll(encoding.encoded, "`", "``") + "`"
					if !testpeer.ServeOK(ctx, t, p, []byte("\x03CREATE DATABASE IF NOT EXISTS "+quoted), 2) || !testpeer.ServeOK(ctx, t, p, append([]byte{2}, encoding.encoded...), 2) || !testpeer.ServeOK(ctx, t, p, []byte("\x03SET @ready = 1"), 2) || !testpeer.ServeOK(ctx, t, p, []byte("\x03"+names), 2) || !testpeer.ServeOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+00:00'"), 2) {
						return
					}
					p.ResetSequence()
					testpeer.ServeQuit(t, p)
				})
				created, err := oceanbase.NewConnector(cfg)
				if err != nil {
					t.Fatal(err)
				}
				c, err := created.Connect(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if err = c.Close(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestCreateDatabaseFailure(t *testing.T) {
	for _, stage := range []string{testCreateDatabaseMode, "create", "select", testCreateDatabaseTimeout} {
		t.Run(stage, func(t *testing.T) {
			var dials atomic.Int32
			cfg := config.Config{User: testUser, Database: "private-target", CreateDatabaseIfMissing: true, Routing: config.RoutingOptions{MaxAttempts: 2}}
			if stage == testCreateDatabaseTimeout {
				cfg.ConnectTimeout = 100 * time.Millisecond
			}
			dial := testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				status := byte(2)
				if stage == testCreateDatabaseMode {
					status = 6
				}
				if !testpeer.ServeCreateDatabaseAuth(ctx, t, p, testStandard, status) {
					return
				}
				if stage != testCreateDatabaseMode {
					if !testpeer.ServeBaseline(ctx, t, p, status) || !testpeer.ReadQuery(ctx, t, p, "CREATE DATABASE IF NOT EXISTS `private-target`") {
						return
					}
					if stage == "select" {
						if !testpeer.SendPackets(ctx, t, p, testpeer.ProtocolOK(2, nil)) || !testpeer.ReadCommand(ctx, t, p, []byte("\x02private-target")) {
							return
						}
					}
					if stage != testCreateDatabaseTimeout && !testpeer.SendPackets(ctx, t, p, []byte{255, 20, 4, '#', '4', '2', '0', '0', '0', 'n', 'o'}) {
						return
					}
				}
				if _, err := p.ReadMessage(ctx); err == nil {
					t.Error("failed initialization continued sending commands")
				}
			})
			cfg.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				dials.Add(1)
				return dial(ctx, network, addr)
			}
			created, err := oceanbase.NewConnector(cfg)
			if err != nil {
				t.Fatal(err)
			}
			_, err = created.Connect(context.Background())
			if dials.Load() != 1 {
				t.Fatal("initialization retried after a possibly effective CREATE", dials.Load())
			}
			var server *oceanbase.Error
			switch stage {
			case testCreateDatabaseMode:
				if !errors.Is(err, dberrors.ErrUnsupported) {
					t.Fatal("Oracle creation not rejected", err)
				}
			case testCreateDatabaseTimeout:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("creation ignored connection budget", err)
				}
			default:
				if !errors.As(err, &server) || server.Code != 1044 {
					t.Fatal("creation/select error hidden", err)
				}
			}
		})
	}
}

func TestCreateDatabaseChangeUser(t *testing.T) {
	created, err := oceanbase.NewConnector(config.Config{User: testUser, Database: testCreateDatabaseName, CreateDatabaseIfMissing: true, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeBaseline(ctx, t, p, 2) || !testpeer.ServeOK(ctx, t, p, []byte(testCreateDatabaseQuery), 2) || !testpeer.ServeOK(ctx, t, p, []byte(testCreateDatabaseSelect), 2) {
			return
		}
		ctx = context.Background()
		if !testpeer.ServeOK(ctx, t, p, changeUserPacket(t, config.ModeMySQL, "next-db"), 2) || !testpeer.ServeBaseline(ctx, t, p, 2) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
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
	pinned, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	if err = oceanbase.ChangeUser(context.Background(), pinned, config.Credentials{User: changeUserName, Password: changeUserPassword}, "next-db"); err != nil {
		t.Fatal(err)
	}
	if err = pinned.PingContext(context.Background()); err != nil {
		t.Fatal("ChangeUser recreated its new database", err)
	}
}

func TestCreateDatabaseCancelQuery(t *testing.T) {
	testCancelQueryIdentity(t, 2, true, false)
}

func TestCreateDatabaseFullReset(t *testing.T) {
	var dials atomic.Int32
	created, err := oceanbase.NewConnector(config.Config{User: testUser, Database: testCreateDatabaseName, CreateDatabaseIfMissing: true, ResetMode: config.ResetFull, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		attempt := dials.Add(1)
		if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeBaseline(ctx, t, p, 2) || !testpeer.ServeOK(ctx, t, p, []byte(testCreateDatabaseQuery), 2) || !testpeer.ServeOK(ctx, t, p, []byte(testCreateDatabaseSelect), 2) {
			return
		}
		ctx = context.Background()
		if !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		if attempt == 1 {
			if _, err := p.ReadMessage(ctx); err == nil {
				t.Error("Full reset retained the original physical session")
			}
		} else {
			testpeer.ServeQuit(t, p)
		}
	})})
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(created)
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	for range 2 {
		if err = db.PingContext(context.Background()); err != nil {
			t.Fatal("replacement did not reapply creation policy", err)
		}
	}
	if dials.Load() != 2 {
		t.Fatal("Full reset did not replace physical connection", dials.Load())
	}
}

func TestCreateDatabaseAuthFailure(t *testing.T) {
	created, err := oceanbase.NewConnector(config.Config{User: testUser, Database: testCreateDatabaseName, CreateDatabaseIfMissing: true, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.SendPackets(ctx, t, p, testpeer.AuthGreeting(authentication.Native, false)) || !testpeer.ReadAuthResponse(ctx, t, p) || !testpeer.SendPackets(ctx, t, p, []byte{255, 21, 4, '#', '2', '8', '0', '0', '0', 'n', 'o'}) {
			return
		}
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("authentication failure still attempted to create a database")
		}
	})})
	if err != nil {
		t.Fatal(err)
	}
	_, err = created.Connect(context.Background())
	var server *oceanbase.Error
	if !errors.As(err, &server) || server.Code != 1045 {
		t.Fatal("authentication failure hidden", err)
	}
}
