package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/cocosip/go-oceanbase"
)

const (
	metadataIndexCase   = "metadata-indexes"
	metadataIndexTable  = "IDX"
	metadataIndexUnique = "UX"
	metadataIndexPlain  = "IX"
	metadataIndexMySQL  = "(ID INTEGER PRIMARY KEY,V VARCHAR(20),W INTEGER)"
	metadataIndexOracle = "(ID INTEGER PRIMARY KEY,V VARCHAR2(20),W INTEGER)"
)

// runMetadataIndexes verifies the index and server-info dictionaries: a unique
// single-column index and a plain multi-column index keep their names,
// uniqueness, column order and sequence, the primary-key index is reported too,
// and the server-info row names the mode the driver is connected in.
func runMetadataIndexes(ctx context.Context, s *scenario) error {
	definition := metadataIndexMySQL
	if s.mode == oceanbase.ModeOracle {
		definition = metadataIndexOracle
	}
	table, err := s.tableNamed(ctx, metadataIndexTable, definition)
	if err != nil {
		return err
	}
	uniqueName, err := createScenarioIndex(ctx, s, table, metadataIndexUnique, "CREATE UNIQUE INDEX %s ON %s (W)")
	if err != nil {
		return err
	}
	plainName, err := createScenarioIndex(ctx, s, table, metadataIndexPlain, "CREATE INDEX %s ON %s (V,W)")
	if err != nil {
		return err
	}
	if err = readMetadataIndexes(ctx, s.db, table, uniqueName, plainName); err != nil {
		return err
	}
	return readMetadataServerInfo(ctx, s.db, s.mode)
}

// createScenarioIndex registers an index with a mode-correct drop statement,
// because MySQL requires the table name in DROP INDEX while Oracle does not.
func createScenarioIndex(ctx context.Context, s *scenario, table, suffix, statement string) (string, error) {
	name := s.prefix + "_" + suffix + "_" + metadataIndexTable
	record := &objectRecord{Kind: "INDEX", Name: name, State: "create_unconfirmed"}
	s.objects = append(s.objects, record)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(statement, name, table)); err != nil {
		return "", err
	}
	record.State = objectStateCreated
	drop := "DROP INDEX " + name
	if s.mode == oceanbase.ModeMySQL {
		drop += " ON " + table
	}
	s.cleanups = append(s.cleanups, cleanupAction{"drop index " + name, func(ctx context.Context) error {
		if _, err := s.db.ExecContext(ctx, drop); err != nil {
			record.State = cleanupFailed
			return err
		}
		record.State = "dropped"
		return nil
	}})
	return name, nil
}

type metadataIndexRow struct {
	name      string
	column    string
	nonUnique int64
	sequence  int64
}

func readMetadataIndexes(ctx context.Context, db *sql.DB, table, uniqueName, plainName string) (err error) {
	return withSession(ctx, db, func(c *sql.Conn) error {
		rows, err := oceanbase.Metadata(ctx, c, oceanbase.MetadataFilter{
			Kind:        oceanbase.MetadataIndexes,
			NamePattern: exactPattern(table),
		})
		if err != nil {
			return err
		}
		defer closeInto(rows, &err)
		var collected []metadataIndexRow
		for rows.Next() {
			var catalog, schema, gotTable, name, column, collation, indexType sql.NullString
			var nonUnique, sequence, cardinality sql.NullInt64
			if err = rows.Scan(&catalog, &schema, &gotTable, &name, &nonUnique, &column, &sequence, &collation, &cardinality, &indexType); err != nil {
				return err
			}
			collected = append(collected, metadataIndexRow{
				name:      name.String,
				column:    column.String,
				nonUnique: nonUnique.Int64,
				sequence:  sequence.Int64,
			})
		}
		if err = rows.Err(); err != nil {
			return err
		}
		unique := metadataIndexByName(collected, uniqueName)
		if len(unique) != 1 || !strings.EqualFold(unique[0].column, "W") || unique[0].nonUnique != 0 || unique[0].sequence != 1 {
			return checkFailure("unique index metadata changed")
		}
		plain := metadataIndexByName(collected, plainName)
		if len(plain) != 2 || !strings.EqualFold(plain[0].column, "V") || !strings.EqualFold(plain[1].column, "W") ||
			plain[0].nonUnique != 1 || plain[1].nonUnique != 1 || plain[0].sequence != 1 || plain[1].sequence != 2 {
			return checkFailure("composite index metadata changed")
		}
		others := 0
		for _, row := range collected {
			if !strings.EqualFold(row.name, uniqueName) && !strings.EqualFold(row.name, plainName) {
				others++
			}
		}
		return expect(others >= 1, "primary-key index metadata missing")
	})
}

func metadataIndexByName(rows []metadataIndexRow, name string) []metadataIndexRow {
	var matched []metadataIndexRow
	for _, row := range rows {
		if strings.EqualFold(row.name, name) {
			matched = append(matched, row)
		}
	}
	return matched
}

func readMetadataServerInfo(ctx context.Context, db *sql.DB, mode oceanbase.Mode) (err error) {
	return withSession(ctx, db, func(c *sql.Conn) error {
		rows, err := oceanbase.Metadata(ctx, c, oceanbase.MetadataFilter{Kind: oceanbase.MetadataServerInfo})
		if err != nil {
			return err
		}
		defer closeInto(rows, &err)
		if !rows.Next() {
			if err = rows.Err(); err != nil {
				return err
			}
			return checkFailure("server info row missing")
		}
		var version, reportedMode, parameters sql.NullString
		var flag sql.NullInt64
		if err = rows.Scan(&version, &reportedMode, &parameters, &flag); err != nil {
			return err
		}
		wantMode := "MYSQL"
		if mode == oceanbase.ModeOracle {
			wantMode = "ORACLE"
		}
		if err = expect(strings.EqualFold(reportedMode.String, wantMode), "server info reported the wrong mode"); err != nil {
			return err
		}
		if err = expect(strings.Contains(version.String, "4.4"), "server info version changed"); err != nil {
			return err
		}
		if rows.Next() {
			return checkFailure("unexpected server info row")
		}
		return rows.Err()
	})
}
