package main

import (
	"context"
	"database/sql"

	"github.com/cocosip/go-oceanbase"
)

const (
	statementParameterCase  = "statement-parameters"
	statementParameterMySQL = "SELECT ?+?"
	statementParameterOra   = "SELECT ?+? FROM dual"
	statementParameterType  = "BIGINT"
)

// runStatementParameters verifies prepared-statement parameter metadata: the
// dictionary reports one row per anonymous placeholder in order, each with a data
// type and bind direction but no name, and the same text really binds values.
func runStatementParameters(ctx context.Context, s *scenario) error {
	query := statementParameterMySQL
	if s.mode == oceanbase.ModeOracle {
		query = statementParameterOra
	}
	var sum int64
	if err := s.db.QueryRowContext(ctx, query, 3, 4).Scan(&sum); err != nil {
		return err
	}
	if err := expect(sum == 7, "statement placeholders bound wrong values"); err != nil {
		return err
	}
	parameters, err := statementParameters(ctx, s.db, query)
	if err != nil {
		return err
	}
	if err = expect(len(parameters) == 2, "statement parameter count changed"); err != nil {
		return err
	}
	nullable := s.mode == oceanbase.ModeOracle
	for i, parameter := range parameters {
		ordinal := int64(i + 1)
		if err = expect(parameter.physical == ordinal && parameter.logical == ordinal,
			"statement parameter ordinals changed"); err != nil {
			return err
		}
		if err = expect(parameter.dataType.String != "" && parameter.protocolType.String == "8" &&
			parameter.protocolDirection.String == "0", "statement parameter type or bind direction changed"); err != nil {
			return err
		}
		if err = expect(!parameter.name.Valid && !parameter.bindDirection.Valid,
			"anonymous placeholder reported a name or bind direction"); err != nil {
			return err
		}
		if err = expect(parameter.nullable.Valid == nullable, "statement parameter nullable flag changed"); err != nil {
			return err
		}
	}
	return nil
}

type statementParameterRow struct {
	physical          int64
	logical           int64
	name              sql.NullString
	bindDirection     sql.NullString
	dataType          sql.NullString
	nullable          sql.NullString
	protocolType      sql.NullString
	protocolDirection sql.NullString
}

func statementParameters(ctx context.Context, db *sql.DB, query string) (rows []statementParameterRow, err error) {
	err = withSession(ctx, db, func(c *sql.Conn) error {
		result, err := oceanbase.Metadata(ctx, c, oceanbase.MetadataFilter{
			Kind:  oceanbase.MetadataStatementParameters,
			Query: query,
		})
		if err != nil {
			return err
		}
		defer closeInto(result, &err)
		for result.Next() {
			var row statementParameterRow
			var length, precision, scale sql.NullInt64
			if err = result.Scan(&row.physical, &row.logical, &row.name, &row.bindDirection, &row.dataType,
				&row.nullable, &length, &precision, &scale, &row.protocolType, &row.protocolDirection); err != nil {
				return err
			}
			rows = append(rows, row)
		}
		return result.Err()
	})
	return rows, err
}
