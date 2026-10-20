package oceanbase

import (
	"context"
	"database/sql/driver"
	"io"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/connect"
	"github.com/cocosip/go-oceanbase/internal/execution"
)

// ErrUnsupported indicates that a requested capability is unavailable.
var ErrUnsupported = dberrors.ErrUnsupported

type connector struct{ runtime *connect.Connector }

// NewConnector validates and snapshots cfg without opening a connection.
func NewConnector(cfg Config) (driver.Connector, error) {
	cfg, err := config.Normalize(cfg)
	if err != nil {
		return nil, err
	}
	runtime, err := connect.New(cfg)
	if err != nil {
		return nil, err
	}
	return &connector{runtime: runtime}, nil
}
func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	physical, err := c.runtime.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &conn{engine: execution.New(c.runtime, physical)}, nil
}

// Close stops shared discovery without ending established business sessions.
func (c *connector) Close() error        { return c.runtime.Close() }
func (*connector) Driver() driver.Driver { return drv{} }

var _ driver.Connector = (*connector)(nil)
var _ io.Closer = (*connector)(nil)
