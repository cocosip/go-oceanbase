//go:build integration

package catalog_test

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/integrationtest"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
)

func TestOracleMetadataOverloads(t *testing.T) {
	profiles, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range profiles {
		if p.Mode == oceanbase.ModeOracle {
			t.Run(p.Name, func(t *testing.T) { integrationOracleOverloads(t, p) })
		}
	}
}

func integrationOracleOverloads(t *testing.T, p integrationtest.Profile) {
	t.Helper()
	db := integrationtest.OpenProfile(t, p)
	ctx := integrationtest.TestContext(t)
	conn, err := db.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "pin overload session", err)
	}
	defer func() { _ = conn.Close() }()
	var schema string
	if err := conn.QueryRowContext(ctx, "SELECT USER FROM dual").Scan(&schema); err != nil {
		integrationtest.FailOperation(t, "read overload schema", err)
	}
	base := fmt.Sprintf("OBG_OV_%d", time.Now().UnixNano())
	object, collection, pkg, proc, fn := base+"O", base+"A", base+"K", base+"P", base+"F"
	for _, definition := range []struct{ kind, name, sql string }{
		{"TYPE", object, "CREATE TYPE " + object + " AS OBJECT (N NUMBER(12,3), V VARCHAR2(20))"},
		{"TYPE", collection, "CREATE TYPE " + collection + " AS TABLE OF NUMBER(15,4)"},
		{"PACKAGE", pkg, "CREATE PACKAGE " + pkg + " AS PROCEDURE " + proc + "(V NUMBER); PROCEDURE " + proc + "(V VARCHAR2); FUNCTION " + fn + "(V " + object + ") RETURN NUMBER; END;"},
	} {
		if _, err := conn.ExecContext(ctx, definition.sql); err != nil {
			integrationtest.FailOperation(t, "create overload fixture", err)
		}
		defer func() {
			if _, err := conn.ExecContext(integrationtest.TestContext(t), "DROP "+definition.kind+" "+definition.name); err != nil {
				integrationtest.FailOperation(t, "drop overload fixture", err)
			}
		}()
	}
	body := "CREATE PACKAGE BODY " + pkg + " AS PROCEDURE " + proc + "(V NUMBER) AS BEGIN IF V!=42 THEN RAISE_APPLICATION_ERROR(-20001,'number overload'); END IF; END; PROCEDURE " + proc + "(V VARCHAR2) AS BEGIN IF V!='text' THEN RAISE_APPLICATION_ERROR(-20001,'text overload'); END IF; END; FUNCTION " + fn + "(V " + object + ") RETURN NUMBER AS BEGIN RETURN V.N; END; END;"
	if _, err := conn.ExecContext(ctx, body); err != nil {
		integrationtest.FailOperation(t, "create overload package body", err)
	}
	if _, err := conn.ExecContext(ctx, "DECLARE N NUMBER; BEGIN "+pkg+"."+proc+"(42); "+pkg+"."+proc+"('text'); N:="+pkg+"."+fn+"("+object+"(12.345,'x')); IF N!=12.345 THEN RAISE_APPLICATION_ERROR(-20001,'object function'); END IF; END;"); err != nil {
		integrationtest.FailOperation(t, "execute independent overloaded PL controls", err)
	}
	filter := oceanbase.MetadataFilter{Kind: oceanbase.MetadataRoutines, SchemaPattern: integrationMetadataPattern(schema), NamePattern: integrationMetadataPattern(base) + "%"}
	identities := integrationOverloadIdentities(t, conn, filter, schema, pkg, proc, fn)
	filter.Kind = oceanbase.MetadataRoutineParameters
	integrationOverloadParameters(t, conn, filter, identities, schema, pkg, fn, object)
	filter.Kind, filter.NamePattern = oceanbase.MetadataTypeAttributes, integrationMetadataPattern(object)
	attrs := integrationMetadataMaps(t, conn, filter)
	if len(attrs) != 2 || attrs[0]["name"] != "N" || attrs[0]["precision"] != "12" || attrs[0]["scale"] != "3" || attrs[1]["name"] != "V" || attrs[1]["length"] != "20" {
		t.Fatal("OBJECT attribute precision or length differs from DDL")
	}
	filter.Kind, filter.NamePattern = oceanbase.MetadataCollectionElements, integrationMetadataPattern(collection)
	elements := integrationMetadataMaps(t, conn, filter)
	if len(elements) != 1 || elements[0]["precision"] != "15" || elements[0]["scale"] != "4" {
		t.Fatal("collection precision differs from DDL")
	}
}

func integrationOverloadIdentities(t *testing.T, conn *sql.Conn, filter oceanbase.MetadataFilter, schema, pkg, proc, fn string) map[string]string {
	t.Helper()
	routines := integrationMetadataMaps(t, conn, filter)
	if len(routines) != 3 {
		t.Fatal("overload routine count", len(routines))
	}
	identities := make(map[string]string)
	for _, r := range routines {
		id := r["specific_name"]
		if id == "" || identities[id] != "" || r["package_name"] != pkg || r["schema"] != schema || r["name"] != proc && r["name"] != fn || (r["type"] == "FUNCTION") != (r["name"] == fn) {
			t.Fatal("overload identity/type mismatch")
		}
		identities[id] = r["name"]
	}
	return identities
}

func integrationOverloadParameters(t *testing.T, conn *sql.Conn, filter oceanbase.MetadataFilter, identities map[string]string, schema, pkg, fn, object string) {
	t.Helper()
	parameters := integrationMetadataMaps(t, conn, filter)
	rootCount, numeric, text, objectInputs, returns := 0, 0, 0, 0, 0
	perIdentity := make(map[string]int)
	for _, r := range parameters {
		if identities[r["specific_name"]] != r["routine_name"] || r["package_name"] != pkg || r["schema"] != schema {
			t.Fatal("parameter identity did not join its routine")
		}
		if r["data_level"] != "0" {
			continue
		}
		rootCount++
		perIdentity[r["specific_name"]]++
		integrationCheckOverloadIdentity(t, conn, r, schema)
		if r["ordinal_position"] == "0" {
			if r["routine_name"] != fn || r["direction"] != "RETURN" || r["data_type"] != valueformat.TypeNameNumber {
				t.Fatal("function return metadata mismatch")
			}
			returns++
			continue
		}
		if r["ordinal_position"] != "1" || r["direction"] != "IN" || r["name"] != "V" {
			t.Fatal("overload argument contract mismatch")
		}
		switch r["data_type"] {
		case valueformat.TypeNameNumber:
			numeric++
		case "VARCHAR2":
			text++
		case "OBJECT":
			integrationCheckObjectParameter(t, conn, r, schema, pkg, fn, object)
			objectInputs++
		default:
			t.Fatal("unexpected overload argument type", r["data_type"])
		}
	}
	if rootCount != 4 || numeric != 1 || text != 1 || objectInputs != 1 || returns != 1 {
		t.Fatal("overload signatures collapsed", rootCount, numeric, text, objectInputs, returns)
	}
	for id, name := range identities {
		want := 1
		if name == fn {
			want = 2
		}
		if perIdentity[id] != want {
			t.Fatal("an overload lost or inherited another overload's parameters")
		}
	}
}

func integrationCheckOverloadIdentity(t *testing.T, conn *sql.Conn, record map[string]string, schema string) {
	t.Helper()
	object, subprogram, ok := strings.Cut(record["specific_name"], ":")
	objectID, objectErr := strconv.ParseInt(object, 10, 64)
	subprogramID, subprogramErr := strconv.ParseInt(subprogram, 10, 64)
	if !ok || objectErr != nil || subprogramErr != nil {
		t.Fatal("invalid specific routine identity")
	}
	var name, typ string
	if err := conn.QueryRowContext(integrationtest.TestContext(t), "SELECT OBJECT_NAME,DATA_TYPE FROM ALL_ARGUMENTS WHERE OWNER=? AND OBJECT_ID=? AND SUBPROGRAM_ID=? AND POSITION=? AND DATA_LEVEL=0", schema, objectID, subprogramID, record["ordinal_position"]).Scan(&name, &typ); err != nil || name != record["routine_name"] || typ != record["data_type"] {
		t.Fatal("parameter does not match the raw dictionary at its specific routine identity", err)
	}
}

func integrationCheckObjectParameter(t *testing.T, conn *sql.Conn, record map[string]string, schema, pkg, fn, object string) {
	t.Helper()
	ctx := integrationtest.TestContext(t)
	var owner, name sql.NullString
	if err := conn.QueryRowContext(ctx, "SELECT TYPE_OWNER,TYPE_NAME FROM ALL_ARGUMENTS WHERE OWNER=? AND PACKAGE_NAME=? AND OBJECT_NAME=? AND POSITION=1 AND DATA_LEVEL=0", schema, pkg, fn).Scan(&owner, &name); err != nil {
		integrationtest.FailOperation(t, "inspect independent OBJECT parameter identity", err)
	}
	_, present := record["type_owner"]
	if record["type_name"] != object || name.String != object || present != owner.Valid || record["type_owner"] != owner.String {
		t.Fatal("OBJECT parameter identity differs from its dictionary")
	}
	var actualOwner string
	if err := conn.QueryRowContext(ctx, "SELECT OWNER FROM ALL_TYPES WHERE OWNER=? AND TYPE_NAME=?", schema, object).Scan(&actualOwner); err != nil || actualOwner != schema {
		t.Fatal("referenced OBJECT type is missing", err)
	}
	if owner.String != schema {
		if _, err := strconv.ParseUint(strings.TrimSpace(owner.String), 10, 64); err != nil {
			t.Fatal("unrecognized dictionary OBJECT owner shape")
		}
		t.Log("ALL_ARGUMENTS.TYPE_OWNER returns a padded numeric identifier for this package OBJECT parameter; metadata preserves the raw dictionary value, not a resolved schema name")
	}
}

func integrationMetadataMaps(t *testing.T, conn *sql.Conn, filter oceanbase.MetadataFilter) []map[string]string {
	t.Helper()
	rows, err := oceanbase.Metadata(integrationtest.TestContext(t), conn, filter)
	if err != nil {
		integrationtest.FailOperation(t, "read metadata boundary", err)
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var result []map[string]string
	for rows.Next() {
		values, dest := make([]sql.NullString, len(columns)), make([]any, len(columns))
		for i := range dest {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			integrationtest.FailOperation(t, "scan metadata boundary", err)
		}
		record := make(map[string]string, len(columns))
		for i, column := range columns {
			if values[i].Valid {
				record[column] = values[i].String
			}
		}
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		integrationtest.FailOperation(t, "consume metadata boundary", err)
	}
	return result
}
