//go:build integration

package valuecodec_test

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestTemporalStorageBoundaries(t *testing.T) {
	profiles, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range profiles {
		t.Run(p.Name, func(t *testing.T) {
			if p.Mode == oceanbase.ModeOracle {
				integrationOracleDSTStorage(t, p)
			} else {
				integrationMySQLZeroDates(t, p)
			}
		})
	}
}

func integrationTemporalSession(t *testing.T, p integrationtest.Profile, prepare oceanbase.PrepareMode, parse bool, loc *time.Location) *sql.Conn {
	t.Helper()
	cfg := integrationtest.ProfileConfig(t, p)
	cfg.PrepareMode, cfg.Session.ParseTime, cfg.Session.Location = prepare, parse, loc
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		integrationtest.FailOperation(t, "temporal boundary connector", err)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	conn, err := db.Conn(integrationtest.TestContext(t))
	if err != nil {
		integrationtest.FailOperation(t, "pin temporal boundary session", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func integrationMySQLZeroDates(t *testing.T, p integrationtest.Profile) {
	t.Helper()
	observer := integrationtest.OpenProfile(t, p)
	ctx := integrationtest.TestContext(t)
	table := fmt.Sprintf("OBG_ZD_%d", time.Now().UnixNano())
	if _, err := observer.ExecContext(ctx, "CREATE TABLE "+table+" (id INTEGER PRIMARY KEY,d DATE,dt DATETIME(6))"); err != nil {
		integrationtest.FailOperation(t, "create zero-date table", err)
	}
	t.Cleanup(func() {
		if _, err := observer.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
			integrationtest.FailOperation(t, "drop zero-date table", err)
		}
	})
	for _, prepare := range []oceanbase.PrepareMode{oceanbase.PrepareServer, oceanbase.PrepareClient} {
		t.Run(fmt.Sprintf("prepare-%d", prepare), func(t *testing.T) {
			conn := integrationTemporalSession(t, p, prepare, false, time.UTC)
			var original string
			if err := conn.QueryRowContext(ctx, "SELECT @@SESSION.sql_mode").Scan(&original); err != nil {
				integrationtest.FailOperation(t, "read original SQL mode", err)
			}
			defer func() {
				if _, err := conn.ExecContext(integrationtest.TestContext(t), "SET SESSION sql_mode=?", original); err != nil {
					integrationtest.FailOperation(t, "restore SQL mode", err)
				}
			}()
			if _, err := conn.ExecContext(ctx, "SET SESSION sql_mode=''"); err != nil {
				integrationtest.FailOperation(t, "set permissive SQL mode", err)
			}
			for i, date := range []string{"0000-00-00", "2024-00-10", "2024-02-00"} {
				id := int(prepare)*10 + i
				stamp := date + " 01:02:03.123456"
				if _, err := conn.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?,?,?)", id, date, stamp); err != nil {
					integrationtest.FailOperation(t, "insert permissive zero date", err)
				}
				var rawDate, rawStamp string
				if err := conn.QueryRowContext(ctx, "SELECT d,dt FROM "+table+" WHERE id=?", id).Scan(&rawDate, &rawStamp); err != nil || rawDate != date || rawStamp != stamp {
					t.Fatal("native zero-date result changed", rawDate, rawStamp, err)
				}
				var storedDate, storedStamp string
				if err := observer.QueryRowContext(ctx, "SELECT CAST(d AS CHAR),CAST(dt AS CHAR) FROM "+table+" WHERE id=?", id).Scan(&storedDate, &storedStamp); err != nil || storedDate != date || storedStamp != stamp {
					t.Fatal("independent zero-date storage", storedDate, storedStamp, err)
				}
				t.Run(fmt.Sprintf("parse-%d", i), func(t *testing.T) {
					parsed := integrationTemporalSession(t, p, prepare, true, time.UTC)
					var instant time.Time
					if err := parsed.QueryRowContext(ctx, "SELECT dt FROM "+table+" WHERE id=?", id).Scan(&instant); !errors.Is(err, oceanbase.ErrType) {
						t.Fatal("unrepresentable zero date was fabricated", instant, err)
					}
				})
			}
			if _, err := conn.ExecContext(ctx, "SET SESSION sql_mode='STRICT_ALL_TABLES,NO_ZERO_DATE,NO_ZERO_IN_DATE'"); err != nil {
				integrationtest.FailOperation(t, "set strict zero-date SQL mode", err)
			}
			for i, date := range []string{"0000-00-00", "2024-00-10", "2024-02-00"} {
				id := int(prepare)*10 + 100 + i
				_, err := conn.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?,?,?)", id, date, date+" 01:02:03.123456")
				if !integrationtest.OnlyServerErrorCode(err, 1292) {
					t.Fatal("strict zero-date binding rejection", err)
				}
				_, err = conn.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s VALUES (%d,'%s','%s 01:02:03.123456')", table, id, date, date))
				if !integrationtest.OnlyServerErrorCode(err, 1292) {
					t.Fatal("strict zero-date SQL control rejection", err)
				}
				var count int
				if err := observer.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE id=?", id).Scan(&count); err != nil || count != 0 {
					t.Fatal("rejected zero-date insert had effects", count, err)
				}
			}
			if err := conn.PingContext(ctx); err != nil {
				integrationtest.FailOperation(t, "reuse strict zero-date session", err)
			}
		})
	}
}

func integrationOracleDSTStorage(t *testing.T, p integrationtest.Profile) {
	t.Helper()
	observer := integrationTemporalSession(t, p, oceanbase.PrepareServer, false, time.UTC)
	ctx := integrationtest.TestContext(t)
	table := fmt.Sprintf("OBG_DST_%d", time.Now().UnixNano())
	if _, err := observer.ExecContext(ctx, "CREATE TABLE "+table+" (id INTEGER PRIMARY KEY,l TIMESTAMP(9) WITH LOCAL TIME ZONE,z TIMESTAMP(9) WITH TIME ZONE)"); err != nil {
		integrationtest.FailOperation(t, "create DST table", err)
	}
	t.Cleanup(func() {
		if _, err := observer.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
			integrationtest.FailOperation(t, "drop DST table", err)
		}
	})
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	regionAvailable := integrationOracleRegionSession(t, p, observer, loc)
	inputs := []time.Time{
		time.Date(2024, 3, 10, 6, 59, 59, 123456789, time.UTC),
		time.Date(2024, 3, 10, 7, 0, 0, 123456789, time.UTC),
		time.Date(2024, 11, 3, 5, 30, 0, 123456789, time.UTC),
		time.Date(2024, 11, 3, 6, 30, 0, 123456789, time.UTC),
	}
	for _, prepare := range []oceanbase.PrepareMode{oceanbase.PrepareServer, oceanbase.PrepareClient} {
		t.Run(fmt.Sprintf("prepare-%d", prepare), func(t *testing.T) {
			writer := integrationTemporalSession(t, p, prepare, false, time.UTC)
			for i, input := range inputs {
				readLocation := loc
				if !regionAvailable {
					_, offset := input.In(loc).Zone()
					readLocation = time.FixedZone("", offset)
				}
				text := integrationTemporalSession(t, p, prepare, false, readLocation)
				id := int(prepare)*10 + i
				literal := input.Format("2006-01-02 15:04:05.000000000 -07:00")
				if _, err := writer.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?,?,?)", id, oceanbase.TimestampLTZ(literal), oceanbase.TimestampTZ(literal)); err != nil {
					integrationtest.FailOperation(t, "insert exact DST instant", err)
				}
				var storedLocal, storedZoned string
				if err := observer.QueryRowContext(ctx, "SELECT TO_CHAR(CAST(l AS TIMESTAMP(9)),'YYYY-MM-DD HH24:MI:SS.FF9'),TO_CHAR(SYS_EXTRACT_UTC(z),'YYYY-MM-DD HH24:MI:SS.FF9') FROM "+table+" WHERE id=?", id).Scan(&storedLocal, &storedZoned); err != nil || storedLocal != input.Format("2006-01-02 15:04:05.000000000") || storedZoned != storedLocal {
					t.Fatal("independent UTC storage differs", storedLocal, storedZoned, err)
				}
				var wall oceanbase.TimestampLTZ
				var zoned oceanbase.TimestampTZ
				if err := text.QueryRowContext(ctx, "SELECT l,z FROM "+table+" WHERE id=?", id).Scan(&wall, &zoned); err != nil {
					integrationtest.FailOperation(t, "read DST text", err)
				}
				got, err := zoned.Time()
				if err != nil || !got.Equal(input) {
					t.Fatal("explicit TZ instant changed", zoned, err)
				}
				got, err = wall.Time()
				if regionAvailable && i >= 2 {
					if !errors.Is(err, oceanbase.ErrType) {
						t.Fatal("ambiguous LTZ was guessed", wall, got, err)
					}
				} else if err != nil || !got.Equal(input) {
					t.Fatal("spring LTZ instant changed", wall, got, err)
				}
				t.Run(fmt.Sprintf("parse-%d", i), func(t *testing.T) {
					parsed := integrationTemporalSession(t, p, prepare, true, readLocation)
					var local, explicit time.Time
					err := parsed.QueryRowContext(ctx, "SELECT l,z FROM "+table+" WHERE id=?", id).Scan(&local, &explicit)
					if regionAvailable && i >= 2 {
						if !errors.Is(err, oceanbase.ErrType) {
							t.Fatal("ParseTime guessed ambiguous LTZ", local, err)
						}
					} else if err != nil || !local.Equal(input) || !explicit.Equal(input) || local.Format("-07:00") != input.In(loc).Format("-07:00") {
						t.Fatal("ParseTime DST mismatch", local, explicit, input, err)
					}
				})
				if err := text.PingContext(ctx); err != nil {
					integrationtest.FailOperation(t, "reuse DST session", err)
				}
			}
		})
	}
}

func integrationOracleRegionSession(t *testing.T, p integrationtest.Profile, control *sql.Conn, loc *time.Location) bool {
	t.Helper()
	cfg := integrationtest.ProfileConfig(t, p)
	cfg.Session.Location = loc
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		integrationtest.FailOperation(t, "named-region connector", err)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	err = db.PingContext(integrationtest.TestContext(t))
	if err == nil {
		return true
	}
	if !integrationtest.OnlyServerErrorCode(err, 1882) {
		integrationtest.FailOperation(t, "named-region session", err)
	}
	_, err = control.ExecContext(integrationtest.TestContext(t), "ALTER SESSION SET TIME_ZONE='America/New_York'")
	if !integrationtest.OnlyServerErrorCode(err, 1882) {
		t.Fatal("named-region SQL control differs from session initialization", err)
	}
	if err := control.PingContext(integrationtest.TestContext(t)); err != nil {
		integrationtest.FailOperation(t, "reuse after named-region rejection", err)
	}
	t.Log("named-region initialization and pure SQL both reject with 1882; positive LTZ storage uses explicit -04:00/-05:00 offsets, not region acceptance")
	return false
}
