package main

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strconv"

	"github.com/cocosip/go-oceanbase"
)

func runGeneratedKeys(ctx context.Context, s *scenario) error {
	table, err := s.table(ctx, "(ID BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,V VARCHAR(100) UNIQUE)")
	if err != nil {
		return err
	}
	return withSession(ctx, s.db, func(c *sql.Conn) error {
		for _, wide := range []bool{false, true} {
			prefix := ""
			if wide {
				if _, err := c.ExecContext(ctx, "ALTER TABLE "+table+" AUTO_INCREMENT=9223372036854775808"); err != nil {
					return err
				}
				prefix = "wide_"
			}
			if err := checkStandardGeneratedKey(ctx, c, table, prefix+"exec", wide); err != nil {
				return err
			}
			if err := checkSummaryGeneratedKey(ctx, c, table, prefix+countExecutionPath, wide); err != nil {
				return err
			}
		}
		var count int
		if err := c.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			return err
		}
		return expect(count == 4, "generated-key row count mismatch")
	})
}

func checkStandardGeneratedKey(ctx context.Context, c *sql.Conn, table, tag string, wide bool) error {
	r, err := c.ExecContext(ctx, "INSERT INTO "+table+" (V) VALUES ('"+tag+"')")
	if err = affected(r, err, 1); err != nil {
		return err
	}
	stored, err := readGeneratedID(ctx, c, table, tag, wide)
	if err != nil {
		return err
	}
	id, idErr := r.LastInsertId()
	if wide {
		err = expect(id == 0 && idErr != nil && !errors.Is(idErr, oceanbase.ErrUnsupported), "unsigned generated ID did not report int64 overflow")
	} else {
		err = expect(idErr == nil && id > 0 && uint64(id) == stored, "generated ID differs from stored primary key")
	}
	if err != nil {
		return err
	}
	r, err = c.ExecContext(ctx, "UPDATE "+table+" SET V='after_"+tag+"' WHERE V='"+tag+"'")
	if err = affected(r, err, 1); err != nil {
		return err
	}
	id, idErr = r.LastInsertId()
	return expect(id == 0 && errors.Is(idErr, oceanbase.ErrUnsupported), "UPDATE fabricated or retained a generated ID")
}

func checkSummaryGeneratedKey(ctx context.Context, c *sql.Conn, table, tag string, wide bool) error {
	info, err := generatedExecution(ctx, c, "INSERT INTO "+table+" (V) VALUES ('"+tag+"')")
	if err != nil {
		return err
	}
	stored, err := readGeneratedID(ctx, c, table, tag, wide)
	if err != nil {
		return err
	}
	if err = expect(info.InsertIDKnown && info.InsertID == strconv.FormatUint(stored, 10), "execution generated ID is unknown or differs from stored primary key"); err != nil {
		return err
	}
	info, err = generatedExecution(ctx, c, "UPDATE "+table+" SET V='after_"+tag+"' WHERE V='"+tag+"'")
	if err != nil {
		return err
	}
	return expect(!info.InsertIDKnown && info.InsertID == "", "UPDATE summary fabricated or retained a generated ID")
}

func readGeneratedID(ctx context.Context, c *sql.Conn, table, tag string, wide bool) (uint64, error) {
	var id uint64
	if err := c.QueryRowContext(ctx, "SELECT ID FROM "+table+" WHERE V='"+tag+"'").Scan(&id); err != nil {
		return 0, err
	}
	return id, expect(id != 0 && (id > math.MaxInt64) == wide, "stored generated ID is outside the expected range")
}

func generatedExecution(ctx context.Context, c *sql.Conn, query string) (info oceanbase.ResultInfo, err error) {
	e, err := oceanbase.Execute(ctx, c, query)
	if e != nil {
		defer closeInto(e, &err)
	}
	if err != nil {
		return info, err
	}
	if err = e.Close(); err != nil {
		return info, err
	}
	snapshot := e.Snapshot()
	if err = snapshot.Err; err != nil {
		return info, err
	}
	if !snapshot.Done || len(snapshot.Results) != 1 {
		return info, checkFailure("generated-key execution has incomplete or extra results")
	}
	info = snapshot.Results[0]
	return info, expect(info.Kind == oceanbase.ResultCommand && info.RowSetIndex == -1 && info.Complete && info.Err == nil && info.RowsAffectedKnown && info.RowsAffected == 1, "generated-key execution count or completion mismatch")
}
