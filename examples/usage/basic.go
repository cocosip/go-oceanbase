package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/cocosip/go-oceanbase"
)

func basic(ctx context.Context, c *sql.Conn, table string, mode oceanbase.Mode) error {
	tx, err := c.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	empty := "X''"
	if mode == oceanbase.ModeOracle {
		empty = "EMPTY_BLOB()"
	}
	query := "INSERT INTO " + table + " (ID,V,B) VALUES(?,?," + empty + ")"
	stmt, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return err
	}
	_, execErr := stmt.ExecContext(ctx, 1, "one")
	if err = errors.Join(execErr, stmt.Close()); err != nil {
		return err
	}
	batch, err := oceanbase.ExecBatch(ctx, tx, query,
		[][]any{{2, "two"}, {3, "three"}},
		oceanbase.BatchOptions{Strategy: oceanbase.BatchAuto})
	for i, item := range batch.Items {
		fmt.Printf("batch input=%d status=%d started=%t\n", i, item.Status, item.Started)
	}
	if err != nil {
		// Never retry an uncertain batch automatically; this example rolls back.
		return err
	}
	fmt.Printf("actual batch strategy=%d; transaction has not committed\n", batch.Strategy)
	if err = tx.Commit(); err != nil {
		// A commit error may leave the outcome unknown; do not replay the writes.
		return err
	}
	var value string
	if err = c.QueryRowContext(ctx, "SELECT V FROM "+table+" WHERE ID=?", 1).Scan(&value); err != nil {
		return err
	}
	if value != "one" {
		return fmt.Errorf("query mismatch: got %q", value)
	}
	fmt.Println("transaction, prepare, batch and query: ok")
	return nil
}

func call(ctx context.Context, c *sql.Conn, name string, mode oceanbase.Mode) (err error) {
	ddl := "CREATE PROCEDURE " + name + "(IN x BIGINT,INOUT y BIGINT,OUT z VARCHAR(40)) BEGIN SET y=x+y; SET z='ok'; END"
	query := "CALL " + name + "(?,?,?)"
	if mode == oceanbase.ModeOracle {
		ddl = "CREATE PROCEDURE " + name + "(x IN NUMBER,y IN OUT NUMBER,z OUT VARCHAR2) AS BEGIN y:=x+y; z:='ok'; END;"
		query = "BEGIN " + name + "(:x,:y,:z); END;"
	}
	if _, err = c.ExecContext(ctx, ddl); err != nil {
		return err
	}
	defer cleanup(c, "DROP PROCEDURE "+name, &err)
	y, z := int64(40), ""
	args := []any{2, sql.Out{Dest: &y, In: true}, oceanbase.Out{Dest: &z, Type: "VARCHAR", Length: 40}}
	if mode == oceanbase.ModeOracle {
		args = []any{sql.Named("x", args[0]), sql.Named("y", args[1]), sql.Named("z", args[2])}
	}
	if _, err = c.ExecContext(ctx, query, args...); err != nil {
		return err
	}
	if y != 42 || z != "ok" {
		return fmt.Errorf("OUT mismatch: got %d, %q", y, z)
	}
	fmt.Println("procedure OUT/INOUT: ok")
	return nil
}

func results(ctx context.Context, c *sql.Conn, table string, mode oceanbase.Mode) (err error) {
	query := "UPDATE " + table + " SET V='ONE' WHERE ID=1; SELECT ID,V FROM " + table + " ORDER BY ID; UPDATE " + table + " SET V='one' WHERE ID=1"
	if mode == oceanbase.ModeOracle {
		query = "BEGIN UPDATE " + table + " SET V='ONE' WHERE ID=1; UPDATE " + table + " SET V='one' WHERE ID=1; END;"
	}
	execution, err := oceanbase.Execute(ctx, c, query)
	if execution != nil {
		// Execute can return both a partial Execution and an error.
		defer func() { err = errors.Join(err, execution.Close()) }()
	}
	if err != nil {
		return err
	}
	rows := execution.Rows()
	for {
		for rows.Next() {
			var id int
			var value string
			if err = rows.Scan(&id, &value); err != nil {
				return err
			}
			fmt.Println("result row:", id, value)
		}
		if !rows.NextResultSet() {
			break
		}
	}
	if err = errors.Join(rows.Err(), execution.Close()); err != nil {
		return err
	}
	snapshot := execution.Snapshot()
	if snapshot.Err != nil {
		return snapshot.Err
	}
	if !snapshot.Done {
		return errors.New("execution response is incomplete")
	}
	// An Oracle block is one result, not a count for each internal statement.
	fmt.Printf("server-observed results=%d\n", len(snapshot.Results))
	return nil
}
