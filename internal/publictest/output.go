package publictest

import (
	"context"
	"database/sql/driver"
)

type checkedConn struct{ RawConn }

// CheckedConn applies the database/sql argument conversion step for direct driver tests.
func CheckedConn(c RawConn) RawConn { return checkedConn{c} }

func (c checkedConn) check(args []driver.NamedValue) error {
	for i := range args {
		if args[i].Ordinal == 0 {
			args[i].Ordinal = i + 1
		}
		if err := c.CheckNamedValue(&args[i]); err != nil {
			return err
		}
	}
	return nil
}

func (c checkedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := c.check(args); err != nil {
		return nil, err
	}
	return c.RawConn.ExecContext(ctx, query, args)
}

func (c checkedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.check(args); err != nil {
		return nil, err
	}
	return c.RawConn.QueryContext(ctx, query, args)
}
