package cursor_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestCursorParsedTemporal(t *testing.T) {
	for _, typ := range []byte{200, 201, 202} {
		t.Run(fmt.Sprint(typ), func(t *testing.T) {
			const query = "SELECT CREATED_AT FROM t"
			data := []byte{20, 24, 2, 29, 12, 34, 56, 21, 205, 91, 7, 9}
			if typ == 200 {
				data = append(data, 8, 0, 0, 0)
			}
			db := publictest.QueryVersionDB(t, "2.2.4", func(ctx context.Context, p *wire.PacketIO) {
				if !publictest.ServePrepare(ctx, t, p, query, 68, 0, nil) || !testpeer.ReadCommand(ctx, t, p, cursorExecuteFixture(68)) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("CREATED_AT", typ, 63, 0), []byte{254, 0, 0, 70, 0}) {
					return
				}
				row := append(append([]byte{0, 0}, byte(len(data))), data...)
				if !testpeer.ReadCommand(ctx, t, p, publictest.CursorFetchFixture(68, 1)) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("CREATED_AT", typ, 63, 0), []byte{254, 0, 0, 6, 0}, row, []byte{254, 0, 0, 134, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 68)) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			}, func(cfg *config.Config) { cfg.Mode = config.ModeOracle; cfg.Session.ParseTime = true })
			defer func() { _ = db.Close() }()
			conn := publictest.PinnedSession(t, db)
			defer func() { _ = conn.Close() }()
			cur, err := oceanbase.OpenCursor(t.Context(), conn, query, oceanbase.CursorOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cur.Close() }()
			rows, err := cur.Fetch(t.Context(), oceanbase.CursorPosition{}, 1)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rows.Close() }()
			columns, err := rows.ColumnTypes()
			if err != nil || len(columns) != 1 || columns[0].ScanType() != reflect.TypeFor[time.Time]() {
				t.Fatal("cursor scan metadata", columns, err)
			}
			if !rows.Next() {
				t.Fatal(rows.Err())
			}
			var got time.Time
			if err = rows.Scan(&got); err != nil {
				t.Fatal(err)
			}
			loc := time.UTC
			if typ == 200 {
				loc = time.FixedZone("", 8*3600)
			}
			want := time.Date(2024, 2, 29, 12, 34, 56, 123456789, loc)
			if !got.Equal(want) {
				t.Fatal(got, want)
			}
			if rows.Next() || rows.Err() != nil {
				t.Fatal(rows.Err())
			}
		})
	}
}
