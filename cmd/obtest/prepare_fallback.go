package main

import (
	"context"
	"database/sql"
	"errors"
)

// runPrepareFallback verifies that an explicit server PREPARE rejected with
// error 1295 stays a server error by default and becomes a client-executed
// statement when EmulateUnsupportedPstmts is enabled.
func runPrepareFallback(ctx context.Context, s *scenario) error {
	name := s.prefix + "_P"
	createSQL := "CREATE PROCEDURE " + name + "() SELECT 1"
	dropSQL := "DROP PROCEDURE " + name
	if _, err := s.db.PrepareContext(ctx, createSQL); !onlyServerErrorCode(err, 1295) {
		return checkFailure("explicit unsupported prepare was not preserved")
	}
	cfg := s.cfg
	cfg.EmulateUnsupportedPstmts = true
	emu, err := openDatabase(cfg)
	if err != nil {
		return err
	}
	s.cleanups = append(s.cleanups, cleanupAction{"close fallback connector", func(context.Context) error { return emu.Close() }})
	owned := false
	s.cleanups = append(s.cleanups, cleanupAction{"drop fallback procedure", func(ctx context.Context) error {
		if !owned {
			return nil
		}
		drop, err := emu.PrepareContext(ctx, dropSQL)
		if err != nil {
			return err
		}
		_, execErr := drop.ExecContext(ctx)
		return errors.Join(execErr, drop.Close())
	}})
	stmt, err := emu.PrepareContext(ctx, createSQL)
	if err != nil {
		return err
	}
	if _, err = stmt.ExecContext(ctx); err != nil {
		_ = stmt.Close()
		return err
	}
	owned = true
	if err = stmt.Close(); err != nil {
		return err
	}
	s.objects = append(s.objects, &objectRecord{Kind: "PROCEDURE", Name: name, State: objectStateCreated})
	if err = expectRoutineCount(ctx, s.db, name, 1); err != nil {
		return err
	}
	drop, err := emu.PrepareContext(ctx, dropSQL)
	if err != nil {
		return err
	}
	if _, err = drop.ExecContext(ctx); err != nil {
		_ = drop.Close()
		return err
	}
	if err = drop.Close(); err != nil {
		return err
	}
	owned = false
	return expectRoutineCount(ctx, s.db, name, 0)
}

func expectRoutineCount(ctx context.Context, db *sql.DB, name string, want int) error {
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = DATABASE() AND ROUTINE_NAME = ?", name).Scan(&n); err != nil {
		return err
	}
	return expect(n == want, "fallback routine count mismatch")
}
