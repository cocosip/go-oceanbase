package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/cocosip/go-oceanbase"
)

func checkDictionaryMetadata(ctx context.Context, c *sql.Conn, mode oceanbase.Mode, schema, table string, kind oceanbase.MetadataKind) (err error) {
	rows, err := oceanbase.Metadata(ctx, c, oceanbase.MetadataFilter{Kind: kind, SchemaPattern: exactPattern(schema), NamePattern: exactPattern(table)})
	if err != nil {
		return err
	}
	defer closeInto(rows, &err)
	want := 1
	if kind == oceanbase.MetadataColumns {
		want = 2
	}
	n := 0
	for rows.Next() {
		if n >= want {
			return checkFailure("unexpected dictionary metadata row")
		}
		if err = checkDictionaryMetadataRow(rows, mode, schema, table, kind, n); err != nil {
			return err
		}
		n++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return expect(n == want, "dictionary metadata row count mismatch")
}

func checkDictionaryMetadataRow(rows *sql.Rows, mode oceanbase.Mode, schema, table string, kind oceanbase.MetadataKind, index int) error {
	var catalog, remarks, defaultValue, owner sql.NullString
	var gotSchema, gotTable, name, typeName, keyName string
	var ordinal, length, precision, scale, nullable, autoIncrement, generated sql.NullInt64
	targets := []any{&catalog, &gotSchema, &gotTable}
	switch kind {
	case oceanbase.MetadataTables:
		targets = append(targets, &typeName, &remarks)
	case oceanbase.MetadataColumns:
		targets = append(targets, &name, &ordinal, &typeName, &length, &precision, &scale, &nullable, &defaultValue, &remarks, &owner, &autoIncrement, &generated)
	case oceanbase.MetadataPrimaryKeys:
		targets = append(targets, &keyName, &name, &ordinal)
	default:
		return checkFailure("unexpected dictionary metadata kind")
	}
	if err := rows.Scan(targets...); err != nil {
		return err
	}
	catalogOK := !catalog.Valid
	if mode == oceanbase.ModeMySQL {
		catalogOK = catalog.Valid && catalog.String == "def"
	}
	if !catalogOK || gotSchema != schema || !strings.EqualFold(gotTable, table) {
		return checkFailure("dictionary metadata object mismatch")
	}
	switch kind {
	case oceanbase.MetadataTables:
		return expect(typeName == "TABLE", "dictionary table kind mismatch")
	case oceanbase.MetadataPrimaryKeys:
		return expect(keyName != "" && strings.EqualFold(name, "ID") && ordinal.Valid && ordinal.Int64 == 1, "dictionary primary key mismatch")
	default:
		autoOK := autoIncrement.Valid && autoIncrement.Int64 == 0
		if mode == oceanbase.ModeOracle {
			autoOK = !autoIncrement.Valid
		}
		if !autoOK || !generated.Valid || generated.Int64 != 0 {
			return checkFailure("ordinary dictionary column marked auto-increment or generated")
		}
		if !ordinal.Valid || ordinal.Int64 != int64(index+1) || !nullable.Valid {
			return checkFailure("dictionary column position or nullability missing")
		}
		if index == 0 {
			return checkDictionaryInteger(mode, name, typeName, nullable, precision, scale)
		}
		_, textType, _ := metadataScalarTypes(mode)
		return expect(strings.EqualFold(name, "V") && strings.EqualFold(typeName, textType) && length.Valid && length.Int64 == 100 && nullable.Int64 == 1, "dictionary character column mismatch")
	}
}

func checkDictionaryInteger(mode oceanbase.Mode, name, typeName string, nullable, precision, scale sql.NullInt64) error {
	idType, _, idPrecision := metadataScalarTypes(mode)
	// OceanBase MySQL dictionaries may report INT precision as 10 or 11.
	precisionOK := precision.Valid && (precision.Int64 == idPrecision || mode == oceanbase.ModeMySQL && precision.Int64 == 11)
	if !strings.EqualFold(name, "ID") || !strings.EqualFold(typeName, idType) || nullable.Int64 != 0 || !precisionOK || !scale.Valid || scale.Int64 != 0 {
		return checkFailure(fmt.Sprintf("dictionary integer column mismatch: name=%s type=%s nullable=%v precision=%v scale=%v", name, typeName, nullable, precision, scale))
	}
	return nil
}

func metadataScalarTypes(mode oceanbase.Mode) (idType, textType string, precision int64) {
	if mode == oceanbase.ModeOracle {
		return "NUMBER", "VARCHAR2", 38
	}
	return "INT", "VARCHAR", 10
}
