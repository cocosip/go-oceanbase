package main

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"time"

	"github.com/cocosip/go-oceanbase"
)

func temporalTimeConfig(cfg *oceanbase.Config) {
	scalarConfig(cfg)
	cfg.Session.ParseTime = true
}

func runTemporalTimes(ctx context.Context, s *scenario) error {
	loc := s.cfg.Session.Location
	if loc == nil {
		loc = time.UTC
	}
	stamp := time.Date(2024, 6, 1, 12, 30, 45, 123456789, loc)
	date := time.Date(2024, 2, 29, 0, 0, 0, 0, loc)
	definition := "(ID INTEGER PRIMARY KEY,D DATE,DT DATETIME(6),TS TIMESTAMP(6),TM TIME(6),Y YEAR)"
	values := []any{1, "2024-02-29", stamp, stamp, "-25:00:00.123456", 2024}
	truncated := stamp.Truncate(time.Microsecond)
	want := []time.Time{date, truncated, truncated}
	if s.mode == oceanbase.ModeOracle {
		definition = "(ID INTEGER PRIMARY KEY,D DATE,DT TIMESTAMP(9),TS TIMESTAMP(9) WITH TIME ZONE,LTZ TIMESTAMP(9) WITH LOCAL TIME ZONE)"
		date = time.Date(2024, 2, 29, 12, 34, 56, 0, loc)
		zoned := stamp.In(time.FixedZone("", -1800))
		values = []any{1, date, stamp, oceanbase.TimestampTZ(zoned.Format("2006-01-02 15:04:05.000000000 -07:00")), oceanbase.TimestampLTZ(stamp.UTC().Format("2006-01-02 15:04:05.000000000 -07:00"))}
		want = []time.Time{date, stamp, zoned, stamp}
	}
	table, err := s.table(ctx, definition)
	if err != nil {
		return err
	}
	query := "INSERT INTO " + table + " VALUES (?" + strings.Repeat(",?", len(values)-1) + ")"
	nulls := make([]any, len(values))
	nulls[0] = 2
	for _, args := range [][]any{values, nulls} {
		result, err := s.db.ExecContext(ctx, query, args...)
		if err = affected(result, err, 1); err != nil {
			return err
		}
	}
	return withSession(ctx, s.db, func(c *sql.Conn) error {
		for _, parameterized := range []bool{false, true} {
			if err := readTemporalTimes(ctx, c, table, s.mode, want, parameterized); err != nil {
				return err
			}
		}
		return nil
	})
}

func readTemporalTimes(ctx context.Context, c *sql.Conn, table string, mode oceanbase.Mode, want []time.Time, parameterized bool) (err error) {
	fields, names := "D,DT,TS,TM,Y", []string{"DATE", "DATETIME", "TIMESTAMP", "TIME", "YEAR"}
	if mode == oceanbase.ModeOracle {
		fields, names = "D,DT,TS,LTZ", []string{"DATE", "TIMESTAMP", "TIMESTAMP WITH TIME ZONE", "TIMESTAMP WITH LOCAL TIME ZONE"}
	}
	query, args := "SELECT "+fields+" FROM "+table+" WHERE ID>=1 ORDER BY ID", []any(nil)
	if parameterized {
		query, args = "SELECT "+fields+" FROM "+table+" WHERE ID>=? ORDER BY ID", []any{1}
	}
	rows, err := c.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer closeInto(rows, &err)
	columns, err := rows.ColumnTypes()
	if err != nil {
		return err
	}
	if err := checkTemporalColumns(columns, names, len(want)); err != nil {
		return err
	}
	return scanTemporalTimes(rows, mode, want)
}

func checkTemporalColumns(columns []*sql.ColumnType, names []string, timeColumns int) error {
	if len(columns) != len(names) {
		return checkFailure("temporal column metadata missing")
	}
	for i, column := range columns {
		typ := reflect.TypeFor[time.Time]()
		if i >= timeColumns {
			typ = reflect.TypeFor[string]()
			if i == len(names)-1 {
				typ = reflect.TypeFor[int64]()
			}
		}
		if column.DatabaseTypeName() != names[i] || column.ScanType() != typ {
			return checkFailure("temporal type metadata mismatch")
		}
	}
	return nil
}

func scanTemporalTimes(rows *sql.Rows, mode oceanbase.Mode, want []time.Time) error {
	for index := 0; index < 2; index++ {
		if !rows.Next() {
			if err := rows.Err(); err != nil {
				return err
			}
			return checkFailure("temporal row missing")
		}
		got := make([]sql.NullTime, len(want))
		dest := make([]any, len(got))
		for i := range got {
			dest[i] = &got[i]
		}
		var duration sql.NullString
		var year sql.NullInt64
		if mode == oceanbase.ModeMySQL {
			dest = append(dest, &duration, &year)
		}
		if err := rows.Scan(dest...); err != nil {
			return err
		}
		for i, value := range got {
			if index == 1 {
				if value.Valid {
					return checkFailure("NULL timestamp became valid")
				}
			} else if !value.Valid || !value.Time.Equal(want[i]) || value.Time.Format("-07:00") != want[i].Format("-07:00") {
				return checkFailure("temporal instant, offset or precision mismatch")
			}
		}
		if mode == oceanbase.ModeMySQL {
			if index == 0 && (!duration.Valid || duration.String != "-25:00:00.123456" || !year.Valid || year.Int64 != 2024) || index == 1 && (duration.Valid || year.Valid) {
				return checkFailure("TIME/YEAR semantics or NULL changed")
			}
		}
	}
	if rows.Next() {
		return checkFailure("unexpected temporal row")
	}
	return rows.Err()
}
