package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/cocosip/go-oceanbase"
)

func runConnect(ctx context.Context, s *scenario) error {
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	var version string
	if err := s.db.QueryRowContext(ctx, s.scalar("SELECT ob_version()")).Scan(&version); err != nil {
		return err
	}
	s.observe("oceanbase_version", version)
	return expect(version != "", "OceanBase version is empty")
}

func runCRUD(ctx context.Context, s *scenario) error {
	table, err := s.table(ctx, "(ID INTEGER PRIMARY KEY,V VARCHAR(100))")
	if err != nil {
		return err
	}
	for _, q := range []string{"INSERT INTO " + table + " VALUES (1,'seed')", "UPDATE " + table + " SET V='updated' WHERE ID=1"} {
		r, err := s.db.ExecContext(ctx, q)
		if err = affected(r, err, 1); err != nil {
			return err
		}
	}
	var value string
	if err = s.db.QueryRowContext(ctx, "SELECT V FROM "+table+" WHERE ID=1").Scan(&value); err != nil {
		return err
	}
	if err = expect(value == "updated", "CRUD update was not persisted"); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (1,'duplicate')")
	if !duplicateKey(err, s.mode) {
		return checkFailure("duplicate key did not return the expected server error code")
	}
	if err = countRows(ctx, s.db, table, 1); err != nil {
		return err
	}
	r, err := s.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE ID=1")
	if err = affected(r, err, 1); err != nil {
		return err
	}
	err = s.db.QueryRowContext(ctx, "SELECT V FROM "+table+" WHERE ID=1").Scan(&value)
	return expect(errors.Is(err, sql.ErrNoRows), "deleted row did not return sql.ErrNoRows")
}

func runPrepared(ctx context.Context, s *scenario) (err error) {
	table, err := s.table(ctx, "(ID INTEGER PRIMARY KEY,V VARCHAR(100))")
	if err != nil {
		return err
	}
	statement, err := s.db.PrepareContext(ctx, "INSERT INTO "+table+" VALUES (?,?)")
	if err != nil {
		return err
	}
	defer closeInto(statement, &err)
	values := []any{unicodeValue + "'\\?", nil, seedChanged}
	for i, value := range values {
		r, e := statement.ExecContext(ctx, i+1, value)
		if e = affected(r, e, 1); e != nil {
			return e
		}
	}
	read, err := s.db.PrepareContext(ctx, "SELECT V FROM "+table+" WHERE ID=?")
	if err != nil {
		return err
	}
	defer closeInto(read, &err)
	for i, value := range values {
		var got sql.NullString
		if err = read.QueryRowContext(ctx, i+1).Scan(&got); err != nil {
			return err
		}
		if err = expect(value == nil && !got.Valid || value != nil && got.Valid && got.String == value, "prepared value or NULL changed"); err != nil {
			return err
		}
	}
	if s.mode == oceanbase.ModeOracle {
		var a, b int
		if err = s.db.QueryRowContext(ctx, "SELECT :x,:X FROM dual", sql.Named("x", 42)).Scan(&a, &b); err != nil {
			return err
		}
		return expect(a == 42 && b == 42, "repeated named bind differs")
	}
	return nil
}

func runScalars(ctx context.Context, s *scenario) error {
	definition := "(ID INTEGER PRIMARY KEY,N DECIMAL(38,9),V VARCHAR(100),B VARBINARY(100),D DATE,TS DATETIME(6),Z VARCHAR(10))"
	binary := any([]byte{0, 1, 127, 128, 255})
	if s.mode == oceanbase.ModeOracle {
		definition = "(ID INTEGER PRIMARY KEY,N NUMBER(38,9),V VARCHAR2(100),B RAW(100),D DATE,TS TIMESTAMP(6),Z VARCHAR2(10))"
		binary = oceanbase.Raw{0, 1, 127, 128, 255}
	}
	table, err := s.table(ctx, definition)
	if err != nil {
		return err
	}
	location := s.cfg.Session.Location
	if location == nil {
		location = time.UTC
	}
	instant := time.Date(2026, 1, 2, 3, 4, 5, 123456000, location)
	var dateValue any = "2024-02-29"
	wantDate := "2024-02-29"
	if s.mode == oceanbase.ModeOracle {
		dateValue = time.Date(2024, 2, 29, 0, 0, 0, 0, location)
		wantDate += " 00:00:00"
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?,?,?,?,?,?,?)", 1, oceanbase.Number(exactNumber), unicodeValue, binary, dateValue, instant, nil); err != nil {
		return err
	}
	return withSession(ctx, s.db, func(c *sql.Conn) error {
		for _, parameterized := range []bool{false, true} {
			query, args := "SELECT N,V,B,D,TS,Z FROM "+table+" WHERE ID=1", []any(nil)
			if parameterized {
				query, args = "SELECT N,V,B,D,TS,Z FROM "+table+" WHERE ID=?", []any{1}
			}
			var n oceanbase.Number
			var v, date, timestamp string
			var raw []byte
			var null sql.NullString
			if err := c.QueryRowContext(ctx, query, args...).Scan(&n, &v, &raw, &date, &timestamp, &null); err != nil {
				return err
			}
			got, ok := new(big.Rat).SetString(string(n))
			want, _ := new(big.Rat).SetString(exactNumber)
			if err := expect(ok && got.Cmp(want) == 0 && v == unicodeValue && bytes.Equal(raw, []byte{0, 1, 127, 128, 255}) && !null.Valid, "scalar precision, Unicode, binary or NULL mismatch"); err != nil {
				return err
			}
			if err := expect(date == wantDate && scalarTimestampMatches(timestamp, instant), "date or timestamp mismatch"); err != nil {
				return err
			}
		}
		return nil
	})
}

func scalarTimestampMatches(text string, want time.Time) bool {
	// A canonical 19-byte wall prefix makes the bound reject fractions Go would truncate.
	if len(text) > 29 || !strings.HasPrefix(text, want.Format(time.DateTime)) {
		return false
	}
	parsed, err := time.Parse(time.DateTime, text)
	return err == nil && parsed.Nanosecond() == want.Nanosecond()
}

func runTransactions(ctx context.Context, s *scenario) (err error) {
	table, err := s.table(ctx, "(ID INTEGER PRIMARY KEY)")
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer rollbackInto(tx, &err)
	if _, err = tx.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?)", 1); err != nil {
		return err
	}
	if err = oceanbase.Savepoint(ctx, tx, "before_second"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?)", 2); err != nil {
		return err
	}
	if err = oceanbase.RollbackTo(ctx, tx, "before_second"); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if err = countRows(ctx, s.db, table, 1); err != nil {
		return err
	}
	rolled, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackInto(rolled, &err)
	if _, err = rolled.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?)", 3); err != nil {
		return err
	}
	if err = rolled.Rollback(); err != nil {
		return err
	}
	if err = countRows(ctx, s.db, table, 1); err != nil {
		return err
	}
	readonly, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer rollbackInto(readonly, &err)
	_, writeErr := readonly.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?)", 4)
	if err = expect(writeErr != nil, "read-only transaction allowed a write"); err != nil {
		return err
	}
	if err = readonly.Rollback(); err != nil {
		return err
	}
	return countRows(ctx, s.db, table, 1)
}

func runMetadata(ctx context.Context, s *scenario) error {
	idType := "INTEGER"
	if s.mode == oceanbase.ModeOracle {
		idType = "NUMBER(38,0)"
	}
	table, err := s.table(ctx, "(ID "+idType+" PRIMARY KEY,V VARCHAR(100))")
	if err != nil {
		return err
	}
	return withSession(ctx, s.db, func(c *sql.Conn) (err error) {
		schema, err := oceanbase.CurrentSchema(ctx, c)
		if err != nil {
			return err
		}
		for _, kind := range []oceanbase.MetadataKind{oceanbase.MetadataTables, oceanbase.MetadataColumns, oceanbase.MetadataPrimaryKeys} {
			if err = checkDictionaryMetadata(ctx, c, s.mode, schema, table, kind); err != nil {
				return err
			}
		}
		rows, err := c.QueryContext(ctx, "SELECT ID,V FROM "+table)
		if err != nil {
			return err
		}
		defer closeInto(rows, &err)
		columns, err := rows.ColumnTypes()
		if err != nil {
			return err
		}
		idType, textType, _ := metadataScalarTypes(s.mode)
		if s.mode == oceanbase.ModeMySQL {
			idType = "INTEGER" // Result type names are normalized; dictionary DATA_TYPE is INT.
		}
		return expect(len(columns) == 2 && strings.EqualFold(columns[0].Name(), "ID") && strings.EqualFold(columns[0].DatabaseTypeName(), idType) && strings.EqualFold(columns[1].Name(), "V") && strings.EqualFold(columns[1].DatabaseTypeName(), textType), "ColumnTypes lost column metadata")
	})
}

func runExecution(ctx context.Context, s *scenario) error {
	return withSession(ctx, s.db, func(c *sql.Conn) (err error) {
		e, err := oceanbase.Execute(ctx, c, s.scalar("SELECT 42 AS V"))
		if err != nil {
			return err
		}
		defer closeInto(e, &err)
		rows := e.Rows()
		if rows == nil || !rows.Next() {
			return checkFailure("execution row missing")
		}
		var n int
		if err = rows.Scan(&n); err != nil {
			return err
		}
		if err = expect(n == 42, "execution row value mismatch"); err != nil {
			return err
		}
		if err = e.Close(); err != nil {
			return err
		}
		snapshot := e.Snapshot()
		if err = snapshot.Err; err != nil {
			return err
		}
		if err = expect(snapshot.Done && len(snapshot.Results) == 1 && snapshot.Results[0].Complete && snapshot.Results[0].Kind == oceanbase.ResultRows, "execution snapshot incomplete"); err != nil {
			return err
		}
		return c.PingContext(ctx)
	})
}

func runBatch(ctx context.Context, s *scenario) (err error) {
	table, err := s.table(ctx, "(ID INTEGER PRIMARY KEY,V VARCHAR(100))")
	if err != nil {
		return err
	}
	for _, strategy := range []oceanbase.BatchStrategy{oceanbase.BatchSequential, oceanbase.BatchMultiSend, oceanbase.BatchRewrite, oceanbase.BatchBulk, oceanbase.BatchArrayBinding} {
		for _, keepGoing := range []bool{false, true} {
			r, e := oceanbase.ExecBatch(ctx, s.db, "INSERT INTO "+table+" VALUES (?,?)", [][]any{{1, seedOne}, {1, "duplicate"}, {2, unicodeValue}}, oceanbase.BatchOptions{Strategy: strategy, ContinueOnError: keepGoing, ChunkRows: 2, ChunkParameters: 4})
			s.observe(fmt.Sprintf("preferred_%s_continue_%t", strategyName(strategy), keepGoing), strategyName(r.Strategy))
			if err = checkBatchDuplicate(r, e, s.mode, keepGoing); err != nil {
				return err
			}
			want := 1
			if keepGoing {
				want = 2
			}
			if err = countRows(ctx, s.db, table, want); err != nil {
				return err
			}
			if _, err = s.db.ExecContext(ctx, "DELETE FROM "+table); err != nil {
				return err
			}
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackInto(tx, &err)
	if _, err = oceanbase.ExecBatch(ctx, tx, "INSERT INTO "+table+" VALUES (?,?)", [][]any{{3, "three"}, {4, "four"}}, oceanbase.BatchOptions{Strategy: oceanbase.BatchArrayBinding}); err != nil {
		return err
	}
	if err = tx.Rollback(); err != nil {
		return err
	}
	return countRows(ctx, s.db, table, 0)
}
