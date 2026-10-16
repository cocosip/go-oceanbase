package oceanbase

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
)

// ErrUnsupported indicates that a requested capability is unavailable.
var ErrUnsupported = errors.New("oceanbase: unsupported capability")

type connector struct{ cfg Config }

// NewConnector validates and snapshots cfg without opening a connection.
func NewConnector(cfg Config) (driver.Connector, error) {
	cfg, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &connector{cfg: cfg}, nil
}

func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	logOperation(ctx, c.cfg.Logger, slog.LevelDebug, "connect")
	return nil, fmt.Errorf("%w: connection establishment is not implemented", ErrUnsupported)
}

func (*connector) Driver() driver.Driver { return drv{} }

var _ driver.Connector = (*connector)(nil)
