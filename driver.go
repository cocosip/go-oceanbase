package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
)

func init() { sql.Register("oceanbase", drv{}) }

type drv struct{}

func (d drv) Open(name string) (driver.Conn, error) {
	c, err := d.OpenConnector(name)
	if err != nil {
		return nil, err
	}
	return c.Connect(context.Background())
}

func (drv) OpenConnector(name string) (driver.Connector, error) {
	cfg, err := ParseDSN(name)
	if err != nil {
		return nil, err
	}
	return NewConnector(cfg)
}

var _ driver.DriverContext = drv{}
