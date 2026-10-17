// The XA example requires OB_DSN for a disposable, XA-enabled tenant.
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/cocosip/go-oceanbase"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	dsn := os.Getenv("OB_DSN")
	if dsn == "" {
		return errors.New("OB_DSN is required")
	}
	db, err := sql.Open("oceanbase", dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	x := oceanbase.XID{FormatID: 42, GlobalID: make([]byte, 16), BranchID: []byte("example")}
	if _, err = rand.Read(x.GlobalID); err != nil {
		return err
	}
	if err = oceanbase.XAStart(ctx, c, x, oceanbase.XANoFlags); err != nil {
		return err
	}
	// Execute application DML through c, then end. No sql.Tx or raw COMMIT/ROLLBACK.
	if err = oceanbase.XAEnd(ctx, c, x, oceanbase.XASuccess); err != nil {
		return err
	}
	// Deliberately roll back: a durable coordinator must log its decision before 2PC.
	return oceanbase.XARollback(ctx, c, x)
}
