package statement

import (
	"container/list"
	"context"
	"fmt"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
)

func cacheStateError(cache *Cache, capacity int) error {
	if len(cache.entries) != cache.order.Len() || cache.order.Len() > capacity {
		return fmt.Errorf("statement cache exceeds its bound or has inconsistent indexes")
	}
	for entry := cache.order.Front(); entry != nil; entry = entry.Next() {
		s := entry.Value.(*Statement)
		if s.closed || s.closeRequested || s.leases != 0 || cache.entries[s.plan.Query] != entry {
			return fmt.Errorf("cache contains an invalid or leased statement")
		}
	}
	return nil
}

func TestStatementCacheInvariants(t *testing.T) {
	const capacity = 2
	op := testOperation(t, config.Config{Execution: config.ExecutionOptions{StatementCacheSize: capacity, StatementCacheSQLLimit: 1024}}, config.ModeMySQL, "")
	ctx := context.Background()
	var cache Cache
	var statements []*Statement
	for _, query := range []string{"SELECT 1", "SELECT 2", "SELECT 3"} {
		s := Adopt(op, 0, sqlPlan(query), nil)
		s.automatic = true
		statements = append(statements, s)
		if err := cache.Recycle(ctx, op, s, true); err != nil {
			t.Fatal(err)
		}
		if err := cacheStateError(&cache, capacity); err != nil {
			t.Fatal(err)
		}
	}
	if cache.order.Len() != capacity || !statements[0].closed {
		t.Fatal("cache did not evict the oldest statement")
	}
	checkedOut, err := cache.Take(ctx, op, "SELECT 2")
	if err != nil || checkedOut != statements[1] || cache.order.Len() != 1 {
		t.Fatal("take did not transfer ownership", err)
	}
	if err = cacheStateError(&cache, capacity); err != nil {
		t.Fatal(err)
	}
	if err = cache.Recycle(ctx, op, checkedOut, true); err != nil {
		t.Fatal(err)
	}
	if err = cache.Clear(ctx, op); err != nil {
		t.Fatal(err)
	}
	if err = cacheStateError(&cache, capacity); err != nil || cache.order.Len() != 0 || !statements[1].closed || !statements[2].closed {
		t.Fatal("clear retained cached statements", err)
	}
}

func TestStatementCacheGuard(t *testing.T) {
	const clean = "clean"
	for _, name := range []string{clean, "cache", "cached-lease", "closed", "close-requested", "index"} {
		t.Run(name, func(t *testing.T) {
			var cache Cache
			capacity := 0
			if name == "cache" {
				cache.order.PushBack(&Statement{})
			} else if name != clean {
				capacity = 1
				s := &Statement{plan: sqlPlan("SELECT 1")}
				entry := cache.order.PushBack(s)
				cache.entries = map[string]*list.Element{s.plan.Query: entry}
				switch name {
				case "cached-lease":
					s.leases = 1
				case "closed":
					s.closed = true
				case "close-requested":
					s.closeRequested = true
				case "index":
					cache.entries[s.plan.Query] = new(list.Element)
				}
			}
			if err := cacheStateError(&cache, capacity); (err == nil) != (name == clean) {
				t.Fatal("cache guard missed an ownership violation", err)
			}
		})
	}
}
