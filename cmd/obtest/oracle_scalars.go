package main

import (
	"context"
	"database/sql"
	"time"

	"github.com/cocosip/go-oceanbase"
)

func runOracleScalars(ctx context.Context, s *scenario) error {
	table, err := s.table(ctx, "(ID INTEGER PRIMARY KEY,NV NVARCHAR2(100),TZ TIMESTAMP(9) WITH TIME ZONE,LTZ TIMESTAMP(9) WITH LOCAL TIME ZONE,YM INTERVAL YEAR(9) TO MONTH,DS INTERVAL DAY(9) TO SECOND(9))")
	if err != nil {
		return err
	}
	for _, args := range [][]any{
		{1, oceanbase.NString(unicodeValue), oceanbase.TimestampTZ("2026-01-02 03:04:05.123456789 -00:30"), oceanbase.TimestampLTZ("2026-01-02 03:04:05.123456789 +00:00"), oceanbase.IntervalYM("-999999999-11"), oceanbase.IntervalDS("-999999999 23:59:59.123456789")},
		{2, nil, nil, nil, nil, nil},
	} {
		r, e := s.db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?,?,?,?,?,?)", args...)
		if e = affected(r, e, 1); e != nil {
			return e
		}
	}
	return withSession(ctx, s.db, func(c *sql.Conn) error {
		for _, parameterized := range []bool{false, true} {
			if err := readOracleScalars(ctx, c, table, parameterized); err != nil {
				return err
			}
		}
		return nil
	})
}

func readOracleScalars(ctx context.Context, c *sql.Conn, table string, parameterized bool) error {
	query, args := "SELECT NV,TZ,LTZ,YM,DS,ROWID FROM "+table+" WHERE ID=1", []any(nil)
	if parameterized {
		query, args = "SELECT NV,TZ,LTZ,YM,DS,ROWID FROM "+table+" WHERE ID=?", []any{1}
	}
	var national oceanbase.NString
	var tz oceanbase.TimestampTZ
	var ltz oceanbase.TimestampLTZ
	var ym oceanbase.IntervalYM
	var ds oceanbase.IntervalDS
	var rowID oceanbase.RowID
	if err := c.QueryRowContext(ctx, query, args...).Scan(&national, &tz, &ltz, &ym, &ds, &rowID); err != nil {
		return err
	}
	instant := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)
	tzTime, err := tz.Time()
	if err != nil {
		return err
	}
	_, offset := tzTime.Zone()
	ltzTime, err := ltz.Time()
	if err != nil {
		return err
	}
	if err = expect(national == oceanbase.NString(unicodeValue) && tzTime.Equal(instant.Add(30*time.Minute)) && offset == -1800 && ltzTime.Equal(instant) && ym == "-999999999-11" && ds == "-999999999 23:59:59.123456789", "Oracle national text, timestamp or interval mismatch"); err != nil {
		return err
	}
	var id int
	if err = c.QueryRowContext(ctx, "SELECT ID FROM "+table+" WHERE ROWID=?", rowID).Scan(&id); err != nil {
		return err
	}
	if err = expect(id == 1, "ROWID did not identify the seeded row"); err != nil {
		return err
	}
	query, args = "SELECT NV,TZ,LTZ,YM,DS FROM "+table+" WHERE ID=2", nil
	if parameterized {
		query, args = "SELECT NV,TZ,LTZ,YM,DS FROM "+table+" WHERE ID=?", []any{2}
	}
	var nullNational sql.Null[oceanbase.NString]
	var nullTZ sql.Null[oceanbase.TimestampTZ]
	var nullLTZ sql.Null[oceanbase.TimestampLTZ]
	var nullYM sql.Null[oceanbase.IntervalYM]
	var nullDS sql.Null[oceanbase.IntervalDS]
	if err = c.QueryRowContext(ctx, query, args...).Scan(&nullNational, &nullTZ, &nullLTZ, &nullYM, &nullDS); err != nil {
		return err
	}
	return expect(!nullNational.Valid && !nullTZ.Valid && !nullLTZ.Valid && !nullYM.Valid && !nullDS.Valid, "Oracle typed NULL changed")
}
