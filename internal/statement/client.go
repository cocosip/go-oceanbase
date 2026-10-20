package statement

import (
	"fmt"
	"sync/atomic"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// Client retains a client-prepared query and its physical identity scope.
type Client struct {
	query  string
	scope  session.StatementScope
	closed atomic.Bool
}

// PrepareClient validates a client-prepared query and captures its identity scope.
func PrepareClient(op *session.Operation, query string) (*Client, error) {
	if int64(utf8.RuneCountInString(query)) >= op.Config().Limits.MaxMessageBytes {
		return nil, wire.ErrMessageTooLarge
	}
	if _, err := sqltext.BuildBindPlanEscapes(query, op.Protocol().Mode, op.Protocol().OK.Status&512 != 0); err != nil {
		return nil, err
	}
	return &Client{query: query, scope: op.StatementScope()}, nil
}

// Query returns the validated SQL template.
func (s *Client) Query() string { return s.query }

// Close marks the client statement closed.
func (s *Client) Close() { s.closed.Store(true) }

// CheckOpen validates the statement's state and physical identity scope.
func (s *Client) CheckOpen(op *session.Operation) error {
	if s.closed.Load() {
		return fmt.Errorf("oceanbase: statement closed")
	}
	if s.scope != op.StatementScope() {
		return dberrors.ErrResource
	}
	return nil
}
