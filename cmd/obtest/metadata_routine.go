package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/cocosip/go-oceanbase"
)

const (
	metadataRoutineCase       = "metadata-routines"
	metadataRoutineProcSuffix = "PROC"
	metadataRoutineFuncSuffix = "FUNC"
	metadataRoutineReturn     = "RETURN"
)

// runMetadataRoutines verifies the routine and routine-parameter dictionaries:
// a procedure and a function keep their names and kinds, the procedure's IN/OUT
// parameters keep their order and direction, and the function reports a RETURN
// row at position zero next to its argument.
func runMetadataRoutines(ctx context.Context, s *scenario) error {
	procDefinition := "(pIn INTEGER, OUT pOut VARCHAR(20)) BEGIN SET pOut='x'; END"
	functionDefinition := "(pIn INTEGER) RETURNS INTEGER DETERMINISTIC RETURN pIn+1"
	if s.mode == oceanbase.ModeOracle {
		procDefinition = "(pIn INTEGER, pOut OUT VARCHAR2) AS BEGIN pOut:='x'; END;"
		functionDefinition = "(pIn INTEGER) RETURN INTEGER AS BEGIN RETURN pIn+1; END;"
	}
	procedure, err := s.create(ctx, "PROCEDURE", metadataRoutineProcSuffix, procDefinition)
	if err != nil {
		return err
	}
	function, err := s.create(ctx, "FUNCTION", metadataRoutineFuncSuffix, functionDefinition)
	if err != nil {
		return err
	}
	if err = checkRoutineKind(ctx, s.db, procedure, "PROCEDURE"); err != nil {
		return err
	}
	if err = checkRoutineKind(ctx, s.db, function, "FUNCTION"); err != nil {
		return err
	}
	if err = checkRoutineParameters(ctx, s.db, procedure, []routineParameter{
		{name: "pIn", position: 1, direction: "IN"},
		{name: "pOut", position: 2, direction: "OUT"},
	}); err != nil {
		return err
	}
	return checkRoutineParameters(ctx, s.db, function, []routineParameter{
		{name: "", position: 0, direction: metadataRoutineReturn},
		{name: "pIn", position: 1, direction: "IN"},
	})
}

type routineParameter struct {
	name      string
	position  int64
	direction string
}

func checkRoutineKind(ctx context.Context, db *sql.DB, name, kind string) (err error) {
	return withSession(ctx, db, func(c *sql.Conn) error {
		rows, err := oceanbase.Metadata(ctx, c, oceanbase.MetadataFilter{
			Kind:        oceanbase.MetadataRoutines,
			NamePattern: exactPattern(name),
		})
		if err != nil {
			return err
		}
		defer closeInto(rows, &err)
		if !rows.Next() {
			if err = rows.Err(); err != nil {
				return err
			}
			return checkFailure("routine row missing")
		}
		var catalog, schema, routineName, specificName, packageName, routineType, dataType sql.NullString
		if err = rows.Scan(&catalog, &schema, &routineName, &specificName, &packageName, &routineType, &dataType); err != nil {
			return err
		}
		if err = expect(strings.EqualFold(routineName.String, name) && routineType.String == kind,
			fmt.Sprintf("routine %q kind %q changed (want %q/%q)", routineName.String, routineType.String, name, kind)); err != nil {
			return err
		}
		if rows.Next() {
			return checkFailure("unexpected routine row")
		}
		return rows.Err()
	})
}

func checkRoutineParameters(ctx context.Context, db *sql.DB, routine string, want []routineParameter) (err error) {
	return withSession(ctx, db, func(c *sql.Conn) error {
		rows, err := oceanbase.Metadata(ctx, c, oceanbase.MetadataFilter{
			Kind:        oceanbase.MetadataRoutineParameters,
			NamePattern: exactPattern(routine),
		})
		if err != nil {
			return err
		}
		defer closeInto(rows, &err)
		index := 0
		for rows.Next() {
			if index >= len(want) {
				return checkFailure("unexpected routine parameter row")
			}
			var catalog, schema, routineName, specificName, packageName, name, direction, dataType sql.NullString
			var position, length, precision, scale, dataLevel sql.NullInt64
			var typeOwner, typeName sql.NullString
			if err = rows.Scan(&catalog, &schema, &routineName, &specificName, &packageName, &name, &position,
				&direction, &dataType, &length, &precision, &scale, &typeOwner, &typeName, &dataLevel); err != nil {
				return err
			}
			expected := want[index]
			if err = expect(position.Int64 == expected.position && direction.String == expected.direction, "routine parameter position or direction changed"); err != nil {
				return err
			}
			if expected.name == "" {
				if err = expect(!name.Valid, "return parameter should not be named"); err != nil {
					return err
				}
			} else if err = expect(strings.EqualFold(name.String, expected.name), "routine parameter name changed"); err != nil {
				return err
			}
			if err = expect(dataType.String != "", "routine parameter data type missing"); err != nil {
				return err
			}
			index++
		}
		if err = rows.Err(); err != nil {
			return err
		}
		return expect(index == len(want), "routine parameter count changed")
	})
}
