package main

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/cocosip/go-oceanbase"
)

const (
	synonymCase       = "oracle-synonym"
	synonymTableDDL   = "(ID INTEGER PRIMARY KEY,V VARCHAR2(20))"
	synonymValue      = "base"
	synonymWriteValue = "via-synonym"
	synonymViewSQL    = "AS SELECT ID,V FROM %s"
	synonymForSQL     = "FOR %s"
	synonymReadSQL    = "SELECT V FROM %s WHERE ID=?"
	synonymTextSQL    = "SELECT V FROM %s WHERE ID>=1"
	synonymInsertSQL  = "INSERT INTO %s VALUES (?,?)"
	synonymTableRead  = "SELECT V FROM %s WHERE ID=?"
)

// runOracleSynonym verifies that user views and synonyms are reachable through
// ordinary SQL, that a table synonym accepts writes and that the dictionary
// reports the synonym's owner and target object.
func runOracleSynonym(ctx context.Context, s *scenario) error {
	table, err := s.table(ctx, synonymTableDDL)
	if err != nil {
		return err
	}
	if _, err = s.db.ExecContext(ctx, fmt.Sprintf(synonymInsertSQL, table), 1, synonymValue); err != nil {
		return err
	}
	view, err := s.create(ctx, "VIEW", "V1", fmt.Sprintf(synonymViewSQL, table))
	if err != nil {
		return err
	}
	viewSynonym, err := s.create(ctx, "SYNONYM", "S1", fmt.Sprintf(synonymForSQL, view))
	if err != nil {
		return err
	}
	tableSynonym, err := s.create(ctx, "SYNONYM", "S2", fmt.Sprintf(synonymForSQL, table))
	if err != nil {
		return err
	}
	if err = readSynonym(ctx, s.db, fmt.Sprintf(synonymReadSQL, viewSynonym), []any{1}, synonymValue); err != nil {
		return err
	}
	if err = readSynonym(ctx, s.db, fmt.Sprintf(synonymTextSQL, tableSynonym), nil, synonymValue); err != nil {
		return err
	}
	if _, err = s.db.ExecContext(ctx, fmt.Sprintf(synonymInsertSQL, tableSynonym), 2, synonymWriteValue); err != nil {
		return err
	}
	if err = readSynonym(ctx, s.db, fmt.Sprintf(synonymTableRead, table), []any{2}, synonymWriteValue); err != nil {
		return err
	}
	return checkSynonymMetadata(ctx, s.db, viewSynonym, view)
}

func readSynonym(ctx context.Context, db *sql.DB, query string, args []any, want string) error {
	var got string
	if err := db.QueryRowContext(ctx, query, args...).Scan(&got); err != nil {
		return err
	}
	return expect(got == want, "synonym or view returned different content")
}

func checkSynonymMetadata(ctx context.Context, db *sql.DB, synonym, target string) (err error) {
	return withSession(ctx, db, func(c *sql.Conn) error {
		rows, err := oceanbase.Metadata(ctx, c, oceanbase.MetadataFilter{
			Kind:        oceanbase.MetadataSynonyms,
			NamePattern: exactPattern(synonym),
		})
		if err != nil {
			return err
		}
		defer closeInto(rows, &err)
		if !rows.Next() {
			if err = rows.Err(); err != nil {
				return err
			}
			return checkFailure("synonym metadata row missing")
		}
		var catalog, gotSchema, name, tableOwner, tableName, dbLink sql.NullString
		if err = rows.Scan(&catalog, &gotSchema, &name, &tableOwner, &tableName, &dbLink); err != nil {
			return err
		}
		if err = expect(!catalog.Valid && !dbLink.Valid, "synonym catalog or database link fabricated"); err != nil {
			return err
		}
		if err = expect(name.String == synonym, "synonym identity changed"); err != nil {
			return err
		}
		if err = expect(tableOwner.String == gotSchema.String && tableOwner.Valid && tableName.String == target, "synonym target changed"); err != nil {
			return err
		}
		if rows.Next() {
			return checkFailure("unexpected synonym metadata row")
		}
		return rows.Err()
	})
}
