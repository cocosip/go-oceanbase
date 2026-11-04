package main

import (
	"context"
	"database/sql"
	"strings"

	"github.com/cocosip/go-oceanbase"
)

const (
	metadataPatternCase = "metadata-patterns"
	metadataPatternDDL  = "(ID INTEGER PRIMARY KEY)"
)

// runMetadataPatterns verifies the dictionary pattern contract: an escaped
// exact name matches only that object, while the unescaped name keeps its LIKE
// meaning with `_` and `%` acting as wildcards.
func runMetadataPatterns(ctx context.Context, s *scenario) error {
	exact, err := s.tableNamed(ctx, "WILD_1", metadataPatternDDL)
	if err != nil {
		return err
	}
	wildcard, err := s.tableNamed(ctx, "WILDA1", metadataPatternDDL)
	if err != nil {
		return err
	}
	percent, err := s.tableNamed(ctx, "PCTX1", metadataPatternDDL)
	if err != nil {
		return err
	}
	checks := []struct {
		pattern  string
		contains []string
		label    string
	}{
		{exactPattern(exact), []string{exact}, "escaped exact name matched a different object"},
		{exact, []string{exact, wildcard}, "unescaped unterminated pattern lost its wildcard"},
		{exactPattern(wildcard), []string{wildcard}, "escaped wildcard-name matched a different object"},
		{exactPattern(percent), []string{percent}, "escaped percent-name matched a different object"},
		{s.prefix + "_PCT%1", []string{percent}, "unescaped percent pattern lost its wildcard"},
	}
	return withSession(ctx, s.db, func(c *sql.Conn) error {
		for _, check := range checks {
			names, err := metadataTableNames(ctx, c, check.pattern)
			if err != nil {
				return err
			}
			for _, want := range check.contains {
				if err = expect(containsFold(names, want), check.label); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func metadataTableNames(ctx context.Context, session oceanbase.Session, pattern string) (names []string, err error) {
	rows, err := oceanbase.Metadata(ctx, session, oceanbase.MetadataFilter{Kind: oceanbase.MetadataTables, NamePattern: pattern})
	if err != nil {
		return nil, err
	}
	defer closeInto(rows, &err)
	for rows.Next() {
		var catalog, schema, name, tableType, remarks sql.NullString
		if err = rows.Scan(&catalog, &schema, &name, &tableType, &remarks); err != nil {
			return nil, err
		}
		names = append(names, name.String)
	}
	return names, rows.Err()
}

func containsFold(names []string, want string) bool {
	for _, name := range names {
		if strings.EqualFold(name, want) {
			return true
		}
	}
	return false
}
