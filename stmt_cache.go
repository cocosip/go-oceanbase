package oceanbase

import (
	"container/list"
	"context"
	"strings"
)

type statementCache struct {
	entries map[string]*list.Element
	order   list.List // oldest idle statement first; checked-out statements are absent.
}

func (c *conn) cacheableSQL(query string) bool {
	tokens, err := lexSQL(query, c.session.mode, c.session.ok.Status&512 != 0, false)
	if err != nil || len(tokens) == 0 || tokens[0].kind != 'w' {
		return false
	}
	switch strings.ToUpper(query[tokens[0].start:tokens[0].end]) {
	case "SELECT", "INSERT", "UPDATE", "DELETE", "REPLACE":
	default:
		return false
	}
	for _, token := range tokens {
		if token.kind == ';' {
			return false
		}
	}
	return true
}

func (c *conn) takeStatementLocked(ctx context.Context, query string) (*stmt, error) {
	e := c.statements.entries[query]
	if e == nil {
		return nil, nil
	}
	s := e.Value.(*stmt)
	delete(c.statements.entries, query)
	c.statements.order.Remove(e)
	if s.cacheNoBackslash != (c.session.ok.Status&512 != 0) {
		return nil, s.closeLocked(ctx)
	}
	return s, nil
}

func (c *conn) recycleStatementLocked(ctx context.Context, s *stmt, success bool) error {
	if !success || !s.automatic || s.closed || s.closeRequested || s.leases != 0 || c.closed.Load() || c.cfg.Execution.StatementCacheSize == 0 || len(s.plan.query) > c.cfg.Execution.StatementCacheSQLLimit || !c.cacheableSQL(s.plan.query) {
		return s.closeLocked(ctx)
	}
	if c.statements.entries == nil {
		c.statements.entries = make(map[string]*list.Element)
	}
	if old := c.statements.entries[s.plan.query]; old != nil {
		previous := old.Value.(*stmt)
		delete(c.statements.entries, s.plan.query)
		c.statements.order.Remove(old)
		if err := previous.closeLocked(ctx); err != nil {
			return err
		}
	}
	s.cacheNoBackslash = c.session.ok.Status&512 != 0
	c.statements.entries[s.plan.query] = c.statements.order.PushBack(s)
	if c.statements.order.Len() > c.cfg.Execution.StatementCacheSize {
		old := c.statements.order.Front()
		previous := old.Value.(*stmt)
		delete(c.statements.entries, previous.plan.query)
		c.statements.order.Remove(old)
		return previous.closeLocked(ctx)
	}
	return nil
}

func (c *conn) clearStatementsLocked(ctx context.Context) error {
	for c.statements.order.Len() != 0 {
		e := c.statements.order.Front()
		s := e.Value.(*stmt)
		delete(c.statements.entries, s.plan.query)
		c.statements.order.Remove(e)
		if err := s.closeLocked(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (c *conn) prepareAutomatic(ctx context.Context, query string, combined bool) (*stmt, error) {
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.unlock()
	s, err := c.takeStatementLocked(ctx, query)
	if err != nil || s != nil {
		return s, err
	}
	if combined {
		plan, planErr := buildBindPlan(query, c.session.mode)
		if planErr != nil {
			return nil, planErr
		}
		return &stmt{conn: c, plan: plan, automatic: true, epoch: c.statementEpoch}, nil
	}
	s, err = c.prepareServerLocked(ctx, query)
	if s != nil {
		s.automatic = true
	}
	return s, err
}
