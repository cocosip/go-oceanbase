package oceanbase

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

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const (
	testCreateDatabaseMode    = "oracle-mode"
	testCreateDatabaseTimeout = "timeout"
	testCreateDatabaseName    = "example"
	testCreateDatabaseQuery   = "\x03CREATE DATABASE IF NOT EXISTS `example`"
	testCreateDatabaseSelect  = "\x02example"
)

func TestDSNCreateDatabase(t *testing.T) {
	for _, value := range []string{"true", "false"} {
		cfg, err := ParseDSN("oceanbase://app@localhost/example?mode=mysql&createDatabaseIfMissing=" + value)
		if err != nil || cfg.CreateDatabaseIfMissing != (value == "true") {
			t.Fatalf("valid explicit creation policy rejected: %v", err)
		}
	}
	for _, dsn := range []string{
		"oceanbase://app@localhost/?createDatabaseIfMissing=true",
		"oceanbase://app@localhost/example?mode=oracle&createDatabaseIfMissing=true",
		"oceanbase://app@localhost/example?createDatabaseIfMissing=1",
		"oceanbase://app@localhost/example?createDatabaseIfMissing=TRUE",
		"oceanbase://app@localhost/example?createDatabaseIfMissing=",
		"oceanbase://app@localhost/example?createDatabaseIfMissing=true&createDatabaseIfMissing=false",
	} {
		_, err := ParseDSN(dsn)
		var invalid *ConfigError
		if !errors.As(err, &invalid) || strings.Contains(err.Error(), "example") {
			t.Fatalf("invalid policy accepted or target leaked: %v", err)
		}
	}
}

func TestCreateDatabaseConfig(t *testing.T) {
	for _, cfg := range []Config{
		{User: testUser, CreateDatabaseIfMissing: true},
		{User: testUser, Mode: ModeOracle, Database: "private", CreateDatabaseIfMissing: true},
		{User: testUser, Database: "private-create\xff", CreateDatabaseIfMissing: true},
		{User: testUser, Database: "private-create\x00", CreateDatabaseIfMissing: true},
		{User: testUser, Database: "私有", Session: SessionOptions{Charset: "ASCII"}, CreateDatabaseIfMissing: true},
	} {
		_, err := NewConnector(cfg)
		var invalid *ConfigError
		if !errors.As(err, &invalid) || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "私有") {
			t.Fatalf("invalid creation target accepted or leaked: %v", err)
		}
	}
	created, err := NewConnector(Config{User: testUser})
	if err != nil || created.(*connector).cfg.CreateDatabaseIfMissing {
		t.Fatal("database creation is not disabled by default", err)
	}
}

func TestCreateDatabaseHandshake(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, serverDB := range []bool{false, true} {
			t.Run(fmt.Sprintf("%t/%t", enabled, serverDB), func(t *testing.T) {
				_, err := runAuthTest(t, Config{User: testUser, Database: "private-target", CreateDatabaseIfMissing: enabled}, func(ctx context.Context, p *wire.PacketIO) {
					greeting := authGreeting(authNative, false)
					if !serverDB {
						i := bytes.IndexByte(greeting[1:], 0) + 2 + 4 + 8 + 1
						binary.LittleEndian.PutUint16(greeting[i:], binary.LittleEndian.Uint16(greeting[i:])&^uint16(wire.ClientConnectWithDB))
					}
					if !sendPackets(ctx, t, p, greeting) {
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
					sendPackets(ctx, t, p, protocolOK(2, nil))
				})
				if !enabled && !serverDB {
					if !errors.Is(err, ErrUnsupported) {
						t.Fatal("ordinary database capability failure hidden", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func serveCreateDatabaseAuth(ctx context.Context, t *testing.T, p *wire.PacketIO, framing string, status byte) bool {
	t.Helper()
	if !sendPackets(ctx, t, p, protocolGreeting(framing == testCompression)) {
		return false
	}
	data, err := p.ReadMessage(ctx)
	if err != nil || len(data) < 4 || binary.LittleEndian.Uint32(data)&uint32(wire.ClientConnectWithDB) != 0 {
		t.Error("database selected before creation", err)
		return false
	}
	var variables map[string]string
	if framing == testOB20 {
		variables = map[string]string{testProxyCapability: "256"}
	}
	if !sendPackets(ctx, t, p, protocolOK(status, variables)) {
		return false
	}
	switch framing {
	case testCompression:
		err = p.EnableCompression()
	case testOB20:
		err = p.EnableOB20(wire.OB20Options{ConnectionID: 42})
	}
	if err != nil {
		t.Error(err)
		return false
	}
	return true
}

func TestCreateDatabaseInitialization(t *testing.T) {
	for _, framing := range []string{testStandard, testCompression, testOB20} {
		for _, encoding := range []struct{ charset, name, encoded string }{
			{charsetUTF8MB4, "db`x;--", "db`x;--"},
			{localFilenameCharset, "测试`x", "\xb2\xe2\xca\xd4`x"},
		} {
			t.Run(framing+"/"+encoding.charset, func(t *testing.T) {
				cfg := Config{User: testUser, Database: encoding.name, CreateDatabaseIfMissing: true, Session: SessionOptions{Charset: encoding.charset, InitSQL: []string{"SET @ready = 1"}}}
				switch framing {
				case testCompression:
					cfg.Protocol.Compression = FeatureRequired
				case testOB20:
					cfg.Protocol.OB20 = FeatureRequired
				}
				cfg.DialContext = peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
					names := "SET NAMES " + encoding.charset
					if !serveCreateDatabaseAuth(ctx, t, p, framing, 2) || !serveOK(ctx, t, p, []byte("\x03"+names), 2) || !serveOK(ctx, t, p, []byte("\x03SET autocommit=1"), 2) || !serveVersion(ctx, t, p, 2, false) || !serveOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+00:00'"), 2) {
						return
					}
					quoted := "`" + strings.ReplaceAll(encoding.encoded, "`", "``") + "`"
					if !serveOK(ctx, t, p, []byte("\x03CREATE DATABASE IF NOT EXISTS "+quoted), 2) || !serveOK(ctx, t, p, append([]byte{2}, encoding.encoded...), 2) || !serveOK(ctx, t, p, []byte("\x03SET @ready = 1"), 2) || !serveOK(ctx, t, p, []byte("\x03"+names), 2) || !serveOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+00:00'"), 2) {
						return
					}
					p.ResetSequence()
					serveQuit(t, p)
				})
				created, err := NewConnector(cfg)
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
			cfg := Config{User: testUser, Database: "private-target", CreateDatabaseIfMissing: true, Routing: RoutingOptions{MaxAttempts: 2}}
			if stage == testCreateDatabaseTimeout {
				cfg.ConnectTimeout = 100 * time.Millisecond
			}
			dial := peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				status := byte(2)
				if stage == testCreateDatabaseMode {
					status = 6
				}
				if !serveCreateDatabaseAuth(ctx, t, p, testStandard, status) {
					return
				}
				if stage != testCreateDatabaseMode {
					if !serveBaseline(ctx, t, p, status) || !readQuery(ctx, t, p, "CREATE DATABASE IF NOT EXISTS `private-target`") {
						return
					}
					if stage == "select" {
						if !sendPackets(ctx, t, p, protocolOK(2, nil)) || !readCommand(ctx, t, p, []byte("\x02private-target")) {
							return
						}
					}
					if stage != testCreateDatabaseTimeout && !sendPackets(ctx, t, p, []byte{255, 20, 4, '#', '4', '2', '0', '0', '0', 'n', 'o'}) {
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
			created, err := NewConnector(cfg)
			if err != nil {
				t.Fatal(err)
			}
			_, err = created.Connect(context.Background())
			if dials.Load() != 1 {
				t.Fatal("initialization retried after a possibly effective CREATE", dials.Load())
			}
			var server *Error
			switch stage {
			case testCreateDatabaseMode:
				if !errors.Is(err, ErrUnsupported) {
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
	created, err := NewConnector(Config{User: testUser, Database: testCreateDatabaseName, CreateDatabaseIfMissing: true, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) || !serveOK(ctx, t, p, []byte(testCreateDatabaseQuery), 2) || !serveOK(ctx, t, p, []byte(testCreateDatabaseSelect), 2) {
			return
		}
		ctx = context.Background()
		if !serveOK(ctx, t, p, changeUserPacket(t, ModeMySQL, "next-db"), 2) || !serveBaseline(ctx, t, p, 2) || !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
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
	if err = ChangeUser(context.Background(), pinned, Credentials{User: changeUserName, Password: changeUserPassword}, "next-db"); err != nil {
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
	created, err := NewConnector(Config{User: testUser, Database: testCreateDatabaseName, CreateDatabaseIfMissing: true, ResetMode: ResetFull, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		attempt := dials.Add(1)
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) || !serveOK(ctx, t, p, []byte(testCreateDatabaseQuery), 2) || !serveOK(ctx, t, p, []byte(testCreateDatabaseSelect), 2) {
			return
		}
		ctx = context.Background()
		if !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		if attempt == 1 {
			if _, err := p.ReadMessage(ctx); err == nil {
				t.Error("Full reset retained the original physical session")
			}
		} else {
			serveQuit(t, p)
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
	created, err := NewConnector(Config{User: testUser, Database: testCreateDatabaseName, CreateDatabaseIfMissing: true, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !sendPackets(ctx, t, p, authGreeting(authNative, false)) || !readAuthResponse(ctx, t, p) || !sendPackets(ctx, t, p, []byte{255, 21, 4, '#', '2', '8', '0', '0', '0', 'n', 'o'}) {
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
	var server *Error
	if !errors.As(err, &server) || server.Code != 1045 {
		t.Fatal("authentication failure hidden", err)
	}
}
