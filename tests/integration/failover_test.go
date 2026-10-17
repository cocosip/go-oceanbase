//go:build integration

package integration

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase"
)

// Each profile needs two real entries for the same tenant and compatibility mode.
func TestConnectionFailover(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) {
			cfg := profileConfig(t, p)
			if len(cfg.Routing.Endpoints) < 2 {
				if os.Getenv("OB_TEST_REQUIRE") == "1" {
					t.Fatal("failover requires two configured addresses for this profile")
				}
				t.Skip("failover requires two configured addresses for this profile")
			}
			cfg.Routing.Endpoints = cfg.Routing.Endpoints[:2]
			cfg.Routing.Role = oceanbase.RoleAny
			cfg.Routing.Strategy = oceanbase.BalanceOrdered
			cfg.Routing.MaxAttempts = 2
			cfg.Routing.RetryDelay = time.Millisecond
			cfg.Routing.BlacklistTimeout = time.Millisecond
			for i := range cfg.Routing.Endpoints {
				cfg.Routing.Endpoints[i].Group = 0
			}
			first, second := cfg.Routing.Endpoints[0].Addr, cfg.Routing.Endpoints[1].Addr
			var reject atomic.Bool
			reject.Store(true)
			var mu sync.Mutex
			var addresses []string
			cfg.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				mu.Lock()
				addresses = append(addresses, addr)
				mu.Unlock()
				if addr == first && reject.Load() {
					return nil, &net.OpError{Op: "dial", Net: network, Err: errors.New("injected unavailable endpoint")}
				}
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			}
			connector, err := oceanbase.NewConnector(cfg)
			if err != nil {
				failOperation(t, "failover configuration", err)
			}
			ctx := testContext(t)
			query := "SELECT 1"
			if p.mode == oceanbase.ModeOracle {
				query += oracleDualSuffix
			}
			db := sql.OpenDB(connector)
			db.SetMaxOpenConns(1)
			defer func() { _ = db.Close() }()
			tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
			if err != nil {
				failOperation(t, "failover begin", err)
			}
			defer func() { _ = tx.Rollback() }()
			reject.Store(false)
			for range 2 {
				var got int
				if err = tx.QueryRowContext(ctx, query).Scan(&got); err != nil {
					failOperation(t, "failover pinned query", err)
				}
				if got != 1 {
					t.Fatal("incorrect query result")
				}
			}
			if err = tx.Rollback(); err != nil {
				failOperation(t, "failover rollback", err)
			}
			mu.Lock()
			initial := append([]string(nil), addresses...)
			mu.Unlock()
			if !reflect.DeepEqual(initial, []string{first, second}) {
				t.Fatal("active session unexpectedly redialed")
			}
			if err = db.Close(); err != nil {
				failOperation(t, "failover close", err)
			}
			timer := time.NewTimer(2 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				t.Fatal("recovery budget expired")
			}
			recovered := sql.OpenDB(connector)
			defer func() { _ = recovered.Close() }()
			if err = recovered.PingContext(ctx); err != nil {
				failOperation(t, "recovered endpoint", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if !reflect.DeepEqual(addresses, []string{first, second, first}) {
				t.Fatal("recovered endpoint not selected")
			}
			t.Log("new connections fail over and recover; active read-only transaction keeps its original socket; lost business responses are covered by protocol tests")
		})
	}
}
