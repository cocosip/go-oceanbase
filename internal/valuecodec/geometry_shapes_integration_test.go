//go:build integration

package valuecodec_test

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"fmt"
	"os"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestMySQLGeometryShapes(t *testing.T) {
	profiles, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range profiles {
		if profile.Mode != oceanbase.ModeMySQL {
			continue
		}
		for _, client := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/client=%t", profile.Name, client), func(t *testing.T) {
				cfg := integrationtest.ProfileConfig(t, profile)
				cfg.PrepareMode = oceanbase.PrepareServer
				if client {
					cfg.PrepareMode = oceanbase.PrepareClient
				}
				connector, err := oceanbase.NewConnector(cfg)
				if err != nil {
					t.Fatal(err)
				}
				db := sql.OpenDB(connector)
				t.Cleanup(func() { _ = db.Close() })
				observer := integrationtest.OpenProfile(t, profile)
				table := fmt.Sprintf("OBGGS_%d", time.Now().UnixNano())
				createTemporalNationalFixture(t, db, "TABLE", table, " (ID INTEGER PRIMARY KEY,G GEOMETRY)")
				for i, wkt := range []string{"LINESTRING(0 0,1 1)", "POLYGON((0 0,2 0,2 2,0 0))", "MULTIPOINT((1 2),(3 4))", "GEOMETRYCOLLECTION(POINT(1 2),LINESTRING(0 0,1 1))"} {
					for j, srid := range []uint32{0, 4326} {
						t.Run(fmt.Sprintf("shape=%d/srid=%d", i, srid), func(t *testing.T) {
							checkGeometryShape(t, db, observer, table, (i*2+j)*2, wkt, srid)
						})
					}
				}
			})
		}
	}
}

func checkGeometryShape(t *testing.T, db, observer *sql.DB, table string, id int, wkt string, srid uint32) {
	t.Helper()
	ctx := integrationtest.TestContext(t)
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, err = conn.ExecContext(ctx, "INSERT INTO "+table+" VALUES(?,ST_GeomFromText(?,?))", id, wkt, srid)
	if srid == 4326 {
		if !integrationtest.OnlyServerErrorCode(err, 3548) {
			integrationtest.FailOperation(t, "expected missing SRS rejection", err)
		}
		// Fixed synthetic WKT contains no quotes; compare the same operation without parameter binding.
		_, err = observer.ExecContext(ctx, "INSERT INTO "+table+" VALUES("+fmt.Sprint(id)+",ST_GeomFromText('"+wkt+"',4326))")
		if !integrationtest.OnlyServerErrorCode(err, 3548) {
			integrationtest.FailOperation(t, "expected SQL missing SRS rejection", err)
		}
		var count int
		if err := observer.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE ID=?", id).Scan(&count); err != nil || count != 0 {
			t.Fatal("missing SRS rejection left a row", count, err)
		}
		if err := conn.PingContext(ctx); err != nil {
			integrationtest.FailOperation(t, "reuse after missing SRS", err)
		}
		return
	}
	if err != nil {
		integrationtest.FailOperation(t, "store geometry shape", err)
	}
	var raw []byte
	var got string
	var gotSRID uint32
	if err := observer.QueryRowContext(ctx, "SELECT G,ST_AsText(G),ST_SRID(G) FROM "+table+" WHERE ID=?", id).Scan(&raw, &got, &gotSRID); err != nil || got != wkt || gotSRID != srid {
		t.Fatal("stored geometry text/SRID changed", got, gotSRID, err)
	}
	if len(raw) < 5 || binary.LittleEndian.Uint32(raw[:4]) != srid {
		t.Fatal("geometry binary SRID changed")
	}
	if _, err := conn.ExecContext(ctx, "INSERT INTO "+table+" VALUES(?,?)", id+1, raw); err != nil {
		integrationtest.FailOperation(t, "rebind geometry shape bytes", err)
	}
	var rebound []byte
	if err := observer.QueryRowContext(ctx, "SELECT G FROM "+table+" WHERE ID=?", id+1).Scan(&rebound); err != nil || !bytes.Equal(raw, rebound) {
		t.Fatal("geometry binary round trip changed", err)
	}
	if err := conn.PingContext(ctx); err != nil {
		integrationtest.FailOperation(t, "reuse after geometry rebind", err)
	}
}
