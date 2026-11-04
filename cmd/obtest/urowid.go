package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/cocosip/go-oceanbase"
)

var urowidSource = strings.Repeat("z", 900)

// runOracleURowID verifies long UROWID capture from the ROWID pseudo column,
// NULL/UROWID round trips through a reused prepared statement, and IN OUT binding
// of the exact value. The long key makes the server return an extended rowid.
func runOracleURowID(ctx context.Context, s *scenario) error {
	table, err := s.table(ctx, "(ID VARCHAR2(1000) PRIMARY KEY,U UROWID(4000))")
	if err != nil {
		return err
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?,NULL)", urowidSource); err != nil {
		return err
	}
	var rowid oceanbase.URowID
	if err = s.db.QueryRowContext(ctx, "SELECT ROWID FROM "+table).Scan(&rowid); err != nil {
		return err
	}
	if len(rowid) <= 251 {
		return checkFailure("source UROWID is not long enough")
	}
	stmt, err := s.db.PrepareContext(ctx, "INSERT INTO "+table+" VALUES (?,?)")
	if err != nil {
		return err
	}
	defer closeInto(stmt, &err)
	for i, want := range []sql.Null[oceanbase.URowID]{{}, {V: rowid, Valid: true}, {}} {
		key := fmt.Sprintf("row-%d", i)
		if _, err = stmt.ExecContext(ctx, key, want); err != nil {
			return err
		}
		var got sql.Null[oceanbase.URowID]
		if err = s.db.QueryRowContext(ctx, "SELECT U FROM "+table+" WHERE ID=?", key).Scan(&got); err != nil {
			return err
		}
		if got != want {
			return checkFailure("UROWID or NULL changed")
		}
		got = want
		if _, err = s.db.ExecContext(ctx, "BEGIN ? := ?; END;", oceanbase.Out{Dest: &got, In: true}, want); err != nil {
			return err
		}
		if got != want {
			return checkFailure("IN OUT UROWID changed")
		}
	}
	var total int
	if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE ID LIKE 'row-%'").Scan(&total); err != nil {
		return err
	}
	return expect(total == 3, "UROWID row count changed")
}
