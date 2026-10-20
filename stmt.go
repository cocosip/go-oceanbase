package oceanbase

import (
	"context"
	"database/sql/driver"

	"github.com/cocosip/go-oceanbase/internal/execution"
	"github.com/cocosip/go-oceanbase/internal/statement"
)

type stmt struct {
	conn   *conn
	server *statement.Statement
}
type clientStmt struct {
	conn   *conn
	client *statement.Client
}

func (c *conn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}
func (c *conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if c.engine.Config().PrepareMode == PrepareClient {
		s, err := c.engine.PrepareClient(ctx, query)
		if err != nil {
			return nil, err
		}
		return &clientStmt{conn: c, client: s}, nil
	}
	s, err := c.engine.Prepare(ctx, query)
	if err != nil {
		return nil, err
	}
	return &stmt{conn: c, server: s}, nil
}
func (s *stmt) NumInput() int { return s.server.Plan().NumInput }
func (s *stmt) Close() error  { return s.conn.engine.CloseStatement(s.server) }
func (s *stmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), namedValues(args))
}
func (s *stmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), namedValues(args))
}
func (s *stmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return s.conn.engine.QueryPrepared(ctx, s.server, args, execution.Options{Outputs: hasOutputArguments(args)})
}
func (s *stmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	rows, err := s.conn.engine.QueryPrepared(ctx, s.server, args, execution.Options{Outputs: hasOutputArguments(args)})
	if err != nil {
		return nil, err
	}
	if err = rows.Drain(ctx); err != nil {
		return nil, err
	}
	return rows.Last(), nil
}
func (s *clientStmt) NumInput() int { return -1 }
func (s *clientStmt) Close() error  { s.client.Close(); return nil }
func (s *clientStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), namedValues(args))
}
func (s *clientStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), namedValues(args))
}
func (s *clientStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return s.conn.engine.QueryClient(ctx, s.client, args, execution.Options{Outputs: hasOutputArguments(args)})
}
func (s *clientStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	rows, err := s.conn.engine.QueryClient(ctx, s.client, args, execution.Options{Outputs: hasOutputArguments(args)})
	if err != nil {
		return nil, err
	}
	if err = rows.Drain(ctx); err != nil {
		return nil, err
	}
	return rows.Last(), nil
}
func namedValues(args []driver.Value) []driver.NamedValue {
	values := make([]driver.NamedValue, len(args))
	for i, arg := range args {
		values[i] = driver.NamedValue{Ordinal: i + 1, Value: arg}
	}
	return values
}

var (
	_ driver.ConnPrepareContext = (*conn)(nil)
	_ driver.Stmt               = (*stmt)(nil)
	_ driver.StmtExecContext    = (*stmt)(nil)
	_ driver.StmtQueryContext   = (*stmt)(nil)
	_ driver.Stmt               = (*clientStmt)(nil)
	_ driver.StmtExecContext    = (*clientStmt)(nil)
	_ driver.StmtQueryContext   = (*clientStmt)(nil)
)
