package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"math/big"

	"github.com/cocosip/go-oceanbase"
)

func tableBlob(ctx context.Context, c *sql.Conn, table string) error {
	tx, err := c.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	blob, err := oceanbase.OpenTableBlob(ctx, tx, oceanbase.BlobTarget{
		Table: table, Column: "B", Key: map[string]any{"ID": 1},
	})
	if err != nil {
		return err
	}
	if blob == nil {
		return errors.New("expected an empty BLOB, got SQL NULL")
	}
	// Writes need the owning transaction; close the handle before committing it.
	if _, err = blob.WriteAtContext(ctx, []byte("hello"), 0); err != nil {
		return errors.Join(err, blob.Close())
	}
	data := make([]byte, 5)
	n, readErr := blob.ReadAtContext(ctx, data, 0)
	if err = errors.Join(readErr, blob.Close()); err != nil {
		return err
	}
	if n != len(data) || string(data) != "hello" {
		return errors.New("table BLOB content mismatch")
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	fmt.Println("table BLOB write/read: ok")
	return nil
}

func nativeBlob(ctx context.Context, c *sql.Conn, table string) (err error) {
	blob, err := oceanbase.OpenBlob(ctx, c, "SELECT B FROM "+table+" WHERE ID=?", 1)
	if err != nil {
		return err
	}
	if blob == nil {
		return errors.New("expected native BLOB, got SQL NULL")
	}
	defer func() { err = errors.Join(err, blob.Close()) }()
	// Read a bounded prefix; streaming a locator does not require full materialization.
	data, err := io.ReadAll(io.LimitReader(blob.Reader(ctx), 5))
	if err != nil {
		return err
	}
	if string(data) != "hello" {
		return errors.New("native BLOB content mismatch")
	}
	fmt.Println("Oracle native BLOB locator: ok")
	return nil
}

func cursor(ctx context.Context, c *sql.Conn, table string) (err error) {
	cur, err := oceanbase.OpenCursor(ctx, c, "SELECT ID,V FROM "+table+" ORDER BY ID",
		oceanbase.CursorOptions{Mode: oceanbase.CursorForward})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, cur.Close()) }()
	count := 0
	for {
		rows, fetchErr := cur.Fetch(ctx, oceanbase.CursorPosition{Origin: oceanbase.CursorNext}, 2)
		if fetchErr != nil {
			return fetchErr
		}
		n := 0
		for rows.Next() {
			var id int
			var value string
			if err = rows.Scan(&id, &value); err != nil {
				return errors.Join(err, rows.Close())
			}
			n++
			fmt.Println("cursor row:", id, value)
		}
		// Release each batch before the next fetch, rather than defer inside the loop.
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		count += n
		if n == 0 {
			break
		}
	}
	if count != 3 {
		return fmt.Errorf("cursor expected 3 rows, got %d", count)
	}
	return nil
}

func complexTypes(ctx context.Context, c *sql.Conn, prefix string) (err error) {
	objectName, arrayName, table := prefix+"O", prefix+"A", prefix+"C"
	if _, err = c.ExecContext(ctx, "CREATE TYPE "+objectName+" AS OBJECT(N NUMBER(28,8))"); err != nil {
		return err
	}
	defer cleanup(c, "DROP TYPE "+objectName, &err)
	if _, err = c.ExecContext(ctx, "CREATE TYPE "+arrayName+" AS TABLE OF "+objectName); err != nil {
		return err
	}
	defer cleanup(c, "DROP TYPE "+arrayName, &err)
	if _, err = c.ExecContext(ctx, "CREATE TABLE "+table+" (ID INTEGER PRIMARY KEY,V "+objectName+",A "+arrayName+")"); err != nil {
		return err
	}
	defer cleanup(c, "DROP TABLE "+table, &err)
	tx, err := c.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	const exact = "12345678901234567890.00123456"
	value := oceanbase.Object{Type: objectName, Fields: []oceanbase.Attribute{
		{Name: "N", Value: oceanbase.Number(exact)},
	}}
	array := oceanbase.Array{Type: arrayName, Elements: []any{value, nil, value}}
	if _, err = tx.ExecContext(ctx, "INSERT INTO "+table+" VALUES(?,?,?)", 1, value, array); err != nil {
		return err
	}
	var got oceanbase.Object
	var items oceanbase.Array
	if err = tx.QueryRowContext(ctx, "SELECT V,A FROM "+table+" WHERE ID=?", 1).Scan(&got, &items); err != nil {
		return err
	}
	if len(got.Fields) != 1 || len(items.Elements) != 3 || items.Elements[1] != nil {
		return errors.New("OBJECT/ARRAY shape or NULL element mismatch")
	}
	number, ok := got.Fields[0].Value.(oceanbase.Number)
	if !ok {
		return errors.New("OBJECT field lost its NUMBER representation")
	}
	want, _ := new(big.Rat).SetString(exact)
	actual, ok := new(big.Rat).SetString(string(number))
	if !ok || actual.Cmp(want) != 0 {
		return errors.New("OBJECT NUMBER precision mismatch")
	}
	fmt.Printf("OBJECT type=%s, NUMBER=%v; ARRAY elements=%d\n", got.Type, got.Fields[0].Value, len(items.Elements))
	// This example discards its data; DDL cleanup still runs outside the transaction.
	return tx.Rollback()
}
