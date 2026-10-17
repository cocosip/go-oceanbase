// The usage example requires an explicit mode and a disposable schema in OB_DSN.
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

func run() (err error) {
	dsn := os.Getenv("OB_DSN")
	if dsn == "" {
		return errors.New("OB_DSN is required; use a disposable schema")
	}
	cfg, err := oceanbase.ParseDSN(dsn)
	if err != nil {
		return err
	}
	if cfg.Mode == oceanbase.ModeAuto {
		return errors.New("set mode=mysql or mode=oracle in OB_DSN; this example uses mode-specific SQL")
	}
	// These demonstrations need server cursors and trusted, parameter-free multi-SQL.
	cfg.PrepareMode = oceanbase.PrepareServer
	cfg.AllowMultiStatements = cfg.Mode == oceanbase.ModeMySQL
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		return err
	}
	db := sql.OpenDB(connector)
	defer func() { err = errors.Join(err, db.Close()) }()
	db.SetMaxOpenConns(2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, c.Close()) }()
	var id [8]byte
	if _, err = rand.Read(id[:]); err != nil {
		return err
	}
	// Only generated identifiers are concatenated; business values are bound below.
	table := fmt.Sprintf("OBGE%X", id)
	blobType, suffix := "LONGBLOB", " ENGINE=InnoDB"
	if cfg.Mode == oceanbase.ModeOracle {
		blobType, suffix = "BLOB", ""
	}
	if _, err = c.ExecContext(ctx, "CREATE TABLE "+table+" (ID INTEGER PRIMARY KEY,V VARCHAR(40),B "+blobType+")"+suffix); err != nil {
		return err
	}
	defer cleanup(c, "DROP TABLE "+table, &err)
	if err = basic(ctx, c, table, cfg.Mode); err != nil {
		return err
	}
	if err = call(ctx, c, table+"P", cfg.Mode); err != nil {
		return err
	}
	if err = results(ctx, c, table, cfg.Mode); err != nil {
		return err
	}
	if err = tableBlob(ctx, c, table); err != nil {
		return err
	}
	if err = cursor(ctx, c, table); err != nil {
		return err
	}
	if cfg.Mode == oceanbase.ModeOracle {
		if err = nativeBlob(ctx, c, table); err != nil {
			return err
		}
		return complexTypes(ctx, c, table)
	}
	fmt.Println("Oracle native locator and OBJECT/ARRAY examples require mode=oracle")
	return nil
}

// DDL may commit implicitly; cleanup uses a fresh budget even if the demo expired.
func cleanup(c *sql.Conn, query string, result *error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.ExecContext(ctx, query)
	if err != nil {
		*result = errors.Join(*result, fmt.Errorf("cleanup %s: %w", query, err))
	}
}
