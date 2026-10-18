package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/cocosip/go-oceanbase"
)

func runSession(ctx context.Context, s *scenario) error {
	info := oceanbase.ClientInfo{ApplicationName: "obtest", ClientUser: "fixture", ClientHostname: "acceptance"}
	err := withSession(ctx, s.db, func(c *sql.Conn) error {
		schema, err := oceanbase.CurrentSchema(ctx, c)
		if err != nil {
			return err
		}
		if err = expect(schema != "", "current schema is empty"); err != nil {
			return err
		}
		catalog, err := oceanbase.CurrentCatalog(ctx, c)
		if s.mode == oceanbase.ModeOracle {
			if !errors.Is(err, oceanbase.ErrUnsupported) {
				return checkFailure("Oracle catalog boundary changed")
			}
		} else if err != nil {
			return err
		} else if catalog != schema {
			return checkFailure("MySQL catalog/schema mismatch")
		}
		if err = oceanbase.SetSchema(ctx, c, schema); err != nil {
			return err
		}
		variable, err := oceanbase.SessionVariable(ctx, c, "wait_timeout")
		if err != nil {
			return err
		}
		if err = expect(variable.Valid, "session variable unavailable"); err != nil {
			return err
		}
		if err = oceanbase.SetSessionVariable(ctx, c, "wait_timeout", variable.String); err != nil {
			return err
		}
		if err = oceanbase.SetClientInfo(ctx, c, info); err != nil {
			return err
		}
		got, err := oceanbase.GetClientInfo(ctx, c)
		if err != nil {
			return err
		}
		return expect(got == info, "client info did not round-trip")
	})
	if err != nil {
		return err
	}
	if err = expect(s.db.Stats().Idle == 0, "mutated session was returned to the original pool"); err != nil {
		return err
	}
	return withSession(ctx, s.db, func(c *sql.Conn) error {
		got, err := oceanbase.GetClientInfo(ctx, c)
		if err != nil {
			return err
		}
		return expect(got != info, "client info leaked to a new borrower")
	})
}

func runWarnings(ctx context.Context, s *scenario) error {
	query := s.target.WarningSQL
	if query == "" {
		if s.mode == oceanbase.ModeOracle {
			return skipped("positive Oracle warning fixture requires warning_sql")
		}
		query = "SELECT CAST('obtest-not-numeric' AS SIGNED)"
	}
	return withSession(ctx, s.db, func(c *sql.Conn) error {
		if _, err := c.ExecContext(ctx, query); err != nil {
			return err
		}
		count, err := oceanbase.Warnings(ctx, c)
		if err != nil {
			return err
		}
		if err = expect(count.Known && count.Count > 0, "warning fixture produced no protocol warnings"); err != nil {
			return err
		}
		details, err := oceanbase.ReadWarnings(ctx, c)
		if s.mode == oceanbase.ModeOracle {
			if !errors.Is(err, oceanbase.ErrUnsupported) {
				return checkFailure("Oracle warning detail boundary changed")
			}
		} else {
			if err != nil {
				return err
			}
			if err = expect(len(details) > 0, "warning details missing"); err != nil {
				return err
			}
		}
		after, err := oceanbase.Warnings(ctx, c)
		if err != nil {
			return err
		}
		if err = expect(after == count, "reading warning details overwrote ordinary warning count"); err != nil {
			return err
		}
		if err = oceanbase.ClearWarnings(ctx, c); err != nil {
			return err
		}
		after, err = oceanbase.Warnings(ctx, c)
		if err != nil {
			return err
		}
		return expect(after.Known && after.Count == 0, "warning count did not clear")
	})
}

func lockedRow(ctx context.Context, s *scenario) (table string, tx *sql.Tx, err error) {
	table, err = s.table(ctx, "(ID INTEGER PRIMARY KEY,V INTEGER)")
	if err != nil {
		return table, nil, err
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (1,0)"); err != nil {
		return table, nil, err
	}
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		return table, nil, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE "+table+" SET V=1 WHERE ID=1"); err != nil {
		return table, tx, err
	}
	return table, tx, nil
}

func runContextCancel(ctx context.Context, s *scenario) (err error) {
	table, holder, err := lockedRow(ctx, s)
	if holder != nil {
		defer rollbackInto(holder, &err)
	}
	if err != nil {
		return err
	}
	waitCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	_, outcome := s.db.ExecContext(waitCtx, "UPDATE "+table+" SET V=2 WHERE ID=1")
	if err = expect(errors.Is(outcome, context.DeadlineExceeded), "lock wait did not end with the caller deadline"); err != nil {
		return err
	}
	if err = holder.Rollback(); err != nil {
		return err
	}
	if err = s.db.PingContext(ctx); err != nil {
		return err
	}
	var value int
	if err = s.db.QueryRowContext(ctx, "SELECT V FROM "+table+" WHERE ID=1").Scan(&value); err != nil {
		return err
	}
	return expect(value == 0, "canceled lock wait changed persisted data")
}

func runCancelQuery(ctx context.Context, s *scenario) (err error) {
	table, holder, err := lockedRow(ctx, s)
	if holder != nil {
		defer rollbackInto(holder, &err)
	}
	if err != nil {
		return err
	}
	configured, err := oceanbase.NewConnector(s.cfg)
	if err != nil {
		return err
	}
	if closer, ok := configured.(interface{ Close() error }); ok {
		defer closeInto(closer, &err)
	}
	err = withSession(ctx, s.db, func(c *sql.Conn) error {
		target, err := oceanbase.QueryTarget(ctx, c)
		if err != nil {
			return err
		}
		query := s.target.CancelSQL
		if query == "" {
			query = "UPDATE " + table + " SET V=2 WHERE ID=1"
		}
		runCtx, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { _, e := c.ExecContext(runCtx, query); done <- e }()
		defer func() { stop(); <-done }()
		// Preserve the result for deferred joining as well as the ordinary result check.
		var outcome error
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			err = oceanbase.CancelQuery(ctx, configured, target)
			if !errors.Is(err, oceanbase.ErrResource) {
				break
			}
			select {
			case outcome = <-done:
				done <- outcome
				return checkFailure("command completed before cancellation became active")
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
		}
		if err != nil {
			return err
		}
		outcome = <-done
		done <- outcome
		if err = checkCancellation(outcome, s.mode, s.target.CancelCode); err != nil {
			return err
		}
		if err = c.PingContext(ctx); err != nil {
			return err
		}
		return expect(errors.Is(oceanbase.CancelQuery(ctx, configured, target), oceanbase.ErrResource), "completed cancellation token remained usable")
	})
	if err != nil {
		return err
	}
	if err = holder.Rollback(); err != nil {
		return err
	}
	var value int
	if err = s.db.QueryRowContext(ctx, "SELECT V FROM "+table+" WHERE ID=1").Scan(&value); err != nil {
		return err
	}
	return expect(value == 0, "explicitly canceled command changed persisted data")
}

func checkCancellation(outcome error, mode oceanbase.Mode, expected uint32) error {
	if expected == 0 {
		expected = 1317
		if mode == oceanbase.ModeOracle {
			expected = 1013
		}
	}
	return expect(onlyServerErrorCode(outcome, expected), "command did not return the expected server cancellation error")
}

func runChangeUser(ctx context.Context, s *scenario) error {
	if s.target.ChangeUserDSNEnv == "" || os.Getenv(s.target.ChangeUserDSNEnv) == "" {
		return skipped("distinct identity requires change_user_dsn_env")
	}
	next, err := oceanbase.ParseDSN(os.Getenv(s.target.ChangeUserDSNEnv))
	if err != nil {
		return checkFailure("invalid change-user DSN")
	}
	identity := s.scalar("SELECT USER")
	if s.mode == oceanbase.ModeMySQL {
		identity = "SELECT CURRENT_USER()"
	}
	var original string
	err = withSession(ctx, s.db, func(c *sql.Conn) (err error) {
		if err = c.QueryRowContext(ctx, identity).Scan(&original); err != nil {
			return err
		}
		old, err := c.PrepareContext(ctx, s.scalar("SELECT 42"))
		if err != nil {
			return err
		}
		defer closeInto(old, &err)
		if err = oceanbase.ChangeUser(ctx, c, oceanbase.Credentials{User: next.User, Password: next.Password, ProxyUser: next.ProxyUser}, next.Database); err != nil {
			return err
		}
		var changed string
		if err = c.QueryRowContext(ctx, identity).Scan(&changed); err != nil {
			return err
		}
		want, _, _ := strings.Cut(next.User, "@")
		if next.ProxyUser != "" {
			want = next.ProxyUser
		}
		actual := changed
		if s.mode == oceanbase.ModeMySQL {
			actual, _, _ = strings.Cut(changed, "@")
		}
		if err = expect(changed != original && actual == want, "distinct change-user identity was not observed"); err != nil {
			return err
		}
		var n int
		if err = old.QueryRowContext(ctx).Scan(&n); !errors.Is(err, oceanbase.ErrResource) {
			return checkFailure("old statement survived identity change")
		}
		err = nil
		if err = c.QueryRowContext(ctx, s.scalar("SELECT 42")).Scan(&n); err != nil {
			return err
		}
		return expect(n == 42, "query as changed identity failed")
	})
	if err != nil {
		return err
	}
	if err = expect(s.db.Stats().Idle == 0, "changed identity returned to original pool"); err != nil {
		return err
	}
	return withSession(ctx, s.db, func(c *sql.Conn) error {
		var got string
		if err := c.QueryRowContext(ctx, identity).Scan(&got); err != nil {
			return err
		}
		return expect(got == original, "original pool identity was not restored")
	})
}

func runLocalImport(ctx context.Context, s *scenario) (err error) {
	command := s.target.LocalImportSQL
	if command == "" {
		if s.mode == oceanbase.ModeOracle {
			return skipped("Oracle LOCAL import requires local_import_sql with {table}")
		}
		command = "LOAD DATA LOCAL INFILE 'input' INTO TABLE {table} FIELDS TERMINATED BY '\\t' LINES TERMINATED BY '\\n' (ID,V)"
	}
	if !strings.Contains(command, "{table}") {
		return checkFailure("local_import_sql requires {table}")
	}
	table, err := s.table(ctx, "(ID INTEGER PRIMARY KEY,V VARCHAR(100))")
	if err != nil {
		return err
	}
	command = strings.ReplaceAll(command, "{table}", table)
	for _, commit := range []bool{false, true} {
		if err = importTransaction(ctx, s, table, command, commit); err != nil {
			return err
		}
	}
	return nil
}

func importTransaction(ctx context.Context, s *scenario, table, command string, commit bool) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackInto(tx, &err)
	r, err := oceanbase.Import(ctx, tx, command, &oceanbase.LocalInfile{Name: "input", Reader: strings.NewReader("1\tone\n2\ttwo\n")})
	if err = affected(r, err, 2); err != nil {
		return err
	}
	var value string
	if err = tx.QueryRowContext(ctx, "SELECT V FROM "+table+" WHERE ID=2").Scan(&value); err != nil {
		return err
	}
	if err = expect(value == seedTwo, "import content mismatch"); err != nil {
		return err
	}
	want := 0
	if commit {
		err, want = tx.Commit(), 2
	} else {
		err = tx.Rollback()
	}
	if err != nil {
		return err
	}
	return countRows(ctx, s.db, table, want)
}
