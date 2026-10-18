package main

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase"
)

const (
	scalarDate      = "2024-02-29"
	scalarTimestamp = "2026-01-02 03:04:05.123456"
)

func TestScalarsTemporalAssertions(t *testing.T) {
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, path := range []string{"text", "parameterized"} {
			for _, tc := range []struct {
				name, date, timestamp string
				wantOK                bool
			}{
				{name: "exact", wantOK: true},
				{name: "equivalent-fraction", timestamp: scalarTimestamp + "000", wantOK: true},
				{name: "date-time-changed", date: scalarDate + " 12:34:56"},
				{name: "date-trailing-junk", date: scalarDate + "invalid"},
				{name: "nanoseconds-changed", timestamp: scalarTimestamp + "001"},
				{name: "timestamp-trailing-junk", timestamp: scalarTimestamp + "invalid"},
				{name: "excess-fraction", timestamp: scalarTimestamp + "0001"},
				{name: "short-hour-excess-fraction", timestamp: "2026-01-02 3:04:05.1234560001"},
			} {
				t.Run(modeName(mode)+"/"+path+"/"+tc.name, func(t *testing.T) {
					s := scalarAssertionScenario(t, mode, path, tc.date, tc.timestamp, tc.wantOK)
					r := runScenario(context.Background(), s, runScalars)
					want := statusFail
					if tc.wantOK {
						want = statusPass
					}
					if r.Status != want || len(r.Objects) != 1 || r.Objects[0].State != fixtureDroppedState || len(r.CleanupErrors) != 0 {
						t.Fatalf("scalar workflow: %+v; want %s and successful cleanup", r, want)
					}
				})
			}
		}
	}
}

func scalarAssertionScenario(t *testing.T, mode oceanbase.Mode, path, dateOverride, timestampOverride string, valid bool) *scenario {
	t.Helper()
	definition := "(ID INTEGER PRIMARY KEY,N DECIMAL(38,9),V VARCHAR(100),B VARBINARY(100),D DATE,TS DATETIME(6),Z VARCHAR(10)) ENGINE=InnoDB"
	date := scalarDate
	if mode == oceanbase.ModeOracle {
		definition = "(ID INTEGER PRIMARY KEY,N NUMBER(38,9),V VARCHAR2(100),B RAW(100),D DATE,TS TIMESTAMP(6),Z VARCHAR2(10))"
		date += " 00:00:00"
	}
	steps := []fixtureStep{
		{query: "CREATE TABLE OBT_OFFLINE_T " + definition},
		{query: "INSERT INTO OBT_OFFLINE_T VALUES (?,?,?,?,?,?,?)", affected: 1},
	}
	for _, parameterized := range []bool{false, true} {
		query := "SELECT N,V,B,D,TS,Z FROM OBT_OFFLINE_T WHERE ID=1"
		valueDate, valueTimestamp := date, scalarTimestamp
		selected := parameterized == (path == "parameterized")
		if selected {
			if dateOverride != "" {
				valueDate = dateOverride
			}
			if timestampOverride != "" {
				valueTimestamp = timestampOverride
			}
		}
		if parameterized {
			query = "SELECT N,V,B,D,TS,Z FROM OBT_OFFLINE_T WHERE ID=?"
		}
		steps = append(steps, fixtureStep{query: query, values: [][]driver.Value{{exactNumber, unicodeValue, []byte{0, 1, 127, 128, 255}, valueDate, valueTimestamp, nil}}})
		if selected && !valid {
			break
		}
	}
	steps = append(steps, fixtureStep{query: "DROP TABLE OBT_OFFLINE_T"})
	s := fixtureScenario(t, steps)
	s.mode = mode
	s.cfg.Session.Location = time.FixedZone("test-offset", 19800)
	return s
}
