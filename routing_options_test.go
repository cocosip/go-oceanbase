package oceanbase

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestRoutingDSN(t *testing.T) {
	cfg, err := ParseDSN("oceanbase://app@baseline/?addresses=first.example,second.example:2882&role=readonly&balance=roundrobin&maxAttempts=3&attemptTimeout=200ms&retryDelay=1ms&blacklistTimeout=10ms&validationInterval=1m")
	if err != nil {
		t.Fatal(err)
	}
	o := cfg.Routing
	if len(o.Endpoints) != 2 || o.Endpoints[0].Addr != routeFirst || o.Endpoints[1].Addr != "second.example:2882" || o.Endpoints[0].Role != RoleReadOnly || o.Role != RoleReadOnly || o.Strategy != BalanceRoundRobin || o.MaxAttempts != 3 || o.AttemptTimeout != 200*time.Millisecond || o.RetryDelay != time.Millisecond || o.BlacklistTimeout != 10*time.Millisecond || o.ValidationInterval != time.Minute {
		t.Fatal(o)
	}
	for _, option := range []string{"addresses=a,,b", "addresses=a,A:2881", "addresses=bad:0", "role=standby", "balance=unknown", "maxAttempts=4097", "maxAttempts=-1", "attemptTimeout=-1s", "retryDelay=bad", "blacklistTimeout=-1s", "validationInterval=-1s"} {
		if _, err = ParseDSN("oceanbase://app@baseline/?" + option); err == nil {
			t.Fatal(option)
		}
	}
}

func TestRoutingSnapshot(t *testing.T) {
	endpoints := []Endpoint{{Addr: routeFirst}, {Addr: routeSecond}}
	created, err := NewConnector(Config{User: testUser, Routing: RoutingOptions{Endpoints: endpoints}})
	if err != nil {
		t.Fatal(err)
	}
	endpoints[0].Addr = "mutated"
	c := created.(*connector)
	got, _ := c.routes.Order(time.Now())
	if got[0].Addr != routeFirst || c.cfg.Routing.Endpoints[0].Addr != routeFirst {
		t.Fatal("mutable routing configuration retained")
	}
	for _, o := range []RoutingOptions{{Role: 99}, {Strategy: 99}, {Endpoints: []Endpoint{{Addr: "a"}, {Addr: "A:2881"}}}, {Endpoints: []Endpoint{{}}}, {Role: RoleReadOnly, Endpoints: []Endpoint{{Addr: routeFirst}}}, {MaxAttempts: 4097}, {ValidationInterval: -1}} {
		if _, err = NewConnector(Config{User: testUser, Routing: o}); err == nil {
			t.Fatal(o)
		}
	}
}

func TestRoutingIdleValidation(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "healthy", true: "stale"}[failed], func(t *testing.T) {
			created, err := NewConnector(Config{User: testUser, Routing: RoutingOptions{ValidationInterval: time.Minute}, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
					return
				}
				ctx = context.Background()
				if !failed {
					if !serveOK(ctx, t, p, []byte{14}, 2) {
						return
					}
					p.ResetSequence()
					serveQuit(t, p)
					return
				}
				if !readCommand(ctx, t, p, []byte{14}) {
					return
				}
			})})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := created.Connect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			c := raw.(*conn)
			// A freshly returned connection must not incur a validation round trip.
			if err = c.ResetSession(context.Background()); err != nil {
				t.Fatal(err)
			}
			c.lastUsed = time.Now().Add(-2 * time.Minute)
			err = c.ResetSession(context.Background())
			if failed {
				if !errors.Is(err, driver.ErrBadConn) || c.IsValid() {
					t.Fatal(err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = c.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRoleReadOnlyTransaction(t *testing.T) {
	c := &conn{cfg: Config{Routing: RoutingOptions{Role: RoleReadOnly}}, session: &handshakeSession{mode: ModeMySQL}}
	if _, err := c.BeginTx(context.Background(), driver.TxOptions{}); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
}
