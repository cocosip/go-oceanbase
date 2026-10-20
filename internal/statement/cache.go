package statement

import (
	"container/list"
	"context"
	"strings"

	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
)

// Cache owns idle automatic statements, ordered from least to most recently recycled.
// Checked-out statements and child-resource leases are never cached.
type Cache struct {
	entries map[string]*list.Element
	order   list.List
}

// CacheableSQL excludes transaction/session changes and multiple statements.
func CacheableSQL(op *session.Operation, query string) bool {
	p := op.Protocol()
	tokens, err := sqltext.LexSQL(query, p.Mode, p.OK.Status&512 != 0, false)
	if err != nil || len(tokens) == 0 || tokens[0].Kind != 'w' {
		return false
	}
	switch strings.ToUpper(query[tokens[0].Start:tokens[0].End]) {
	case "SELECT", "INSERT", "UPDATE", "DELETE", "REPLACE":
	default:
		return false
	}
	for _, token := range tokens {
		if token.Kind == ';' {
			return false
		}
	}
	return true
}

// Take removes one idle statement, rejecting metadata parsed under different escaping.
func (c *Cache) Take(ctx context.Context, op *session.Operation, query string) (*Statement, error) {
	e := c.entries[query]
	if e == nil {
		return nil, nil
	}
	s := e.Value.(*Statement)
	delete(c.entries, query)
	c.order.Remove(e)
	if s.cacheNoBackslash != (op.Protocol().OK.Status&512 != 0) || s.scope != op.StatementScope() {
		return nil, s.Close(ctx, op)
	}
	return s, nil
}

// Recycle returns a successfully completed automatic statement to the bounded idle cache.
func (c *Cache) Recycle(ctx context.Context, op *session.Operation, s *Statement, success bool) error {
	if s == nil {
		return nil
	}
	o := op.Config().Execution
	if !success || !s.automatic || s.closed || s.closeRequested || s.leases != 0 || op.Closed() || s.scope != op.StatementScope() || o.StatementCacheSize == 0 || len(s.plan.Query) > o.StatementCacheSQLLimit || !CacheableSQL(op, s.plan.Query) {
		return s.Close(ctx, op)
	}
	if c.entries == nil {
		c.entries = make(map[string]*list.Element)
	}
	if old := c.entries[s.plan.Query]; old != nil {
		previous := old.Value.(*Statement)
		delete(c.entries, s.plan.Query)
		c.order.Remove(old)
		if err := previous.Close(ctx, op); err != nil {
			return err
		}
	}
	s.cacheNoBackslash = op.Protocol().OK.Status&512 != 0
	c.entries[s.plan.Query] = c.order.PushBack(s)
	if c.order.Len() > o.StatementCacheSize {
		old := c.order.Front()
		previous := old.Value.(*Statement)
		delete(c.entries, previous.plan.Query)
		c.order.Remove(old)
		return previous.Close(ctx, op)
	}
	return nil
}

// Clear closes idle statements in eviction order, stopping at the first close failure.
func (c *Cache) Clear(ctx context.Context, op *session.Operation) error {
	for c.order.Len() != 0 {
		e := c.order.Front()
		s := e.Value.(*Statement)
		delete(c.entries, s.plan.Query)
		c.order.Remove(e)
		if err := s.Close(ctx, op); err != nil {
			return err
		}
	}
	return nil
}
