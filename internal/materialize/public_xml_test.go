package materialize_test

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const testXMLText = "text"
const testXMLServer = "server"
const testXMLCombined = "combined"

func TestXMLTypeSQLResults(t *testing.T) {
	for _, path := range []string{testXMLText, testXMLServer, testXMLCombined} {
		for _, owner := range []string{"", "SYS"} {
			t.Run(path+"/"+owner, func(t *testing.T) {
				query := "SELECT x FROM t"
				if path != testXMLText {
					query += " WHERE id=?"
				}
				const want = "<x>患者😀</x>\n"
				column := testpeer.TextColumn("x", 160, 63, 0)
				column = append(column, byte(len(owner)))
				column = append(column, owner...)
				column = append(column, 7, 'X', 'M', 'L', 'T', 'Y', 'P', 'E', 251)
				db := publictest.OutOracleDB(t, config.PrepareServer, func(ctx context.Context, p *wire.PacketIO) { serveXMLResults(ctx, t, p, path, query, column) }, func(cfg *config.Config) { cfg.Execution.PrepareExecute = path == testXMLCombined })
				defer func() { _ = db.Close() }()
				var rows *sql.Rows
				var stmt *sql.Stmt
				var err error
				if path == testXMLText {
					rows, err = db.QueryContext(context.Background(), query)
				} else {
					if path == testXMLServer {
						var e error
						stmt, e = db.Prepare(query)
						if e != nil {
							t.Fatal(e)
						}
						defer func() { _ = stmt.Close() }()
						rows, err = stmt.Query(1)
					} else {
						rows, err = db.Query(query, 1)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = rows.Close() }()
				columns, err := rows.ColumnTypes()
				if err != nil || columns[0].DatabaseTypeName() != "XMLTYPE" || columns[0].ScanType() != reflect.TypeFor[string]() {
					t.Fatal(columns, err)
				}
				var got sql.Null[string]
				if !rows.Next() {
					t.Fatal(rows.Err())
				}
				if err = rows.Scan(&got); err != nil || !got.Valid || got.V != want {
					t.Fatal(got, err)
				}
				if !rows.Next() {
					t.Fatal(rows.Err())
				}
				if err = rows.Scan(&got); err != nil || got.Valid {
					t.Fatal(got, err)
				}
				if rows.Next() || rows.Err() != nil {
					t.Fatal(rows.Err())
				}
				if err = rows.Close(); err != nil {
					t.Fatal(err)
				}
				if stmt != nil {
					if err = stmt.Close(); err != nil {
						t.Fatal(err)
					}
				}
				if err = db.Ping(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestXMLTypeRejectsInvalidUTF8(t *testing.T) {
	db := publictest.OutOracleDB(t, config.PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
		column := append(testpeer.TextColumn("x", 160, 63, 0), 0, 7, 'X', 'M', 'L', 'T', 'Y', 'P', 'E', 251)
		if !testpeer.ReadQuery(ctx, t, p, "SELECT badxml") || !testpeer.SendPackets(ctx, t, p, []byte{1}, column, []byte{254, 0, 0, 6, 0}, []byte{1, 255}) {
			return
		}
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("malformed XML connection reused")
		}
	})
	defer func() { _ = db.Close() }()
	var got string
	if err := db.QueryRow("SELECT badxml").Scan(&got); !errors.Is(err, charset.ErrEncoding) {
		t.Fatal(got, err)
	}
}

func serveXMLResults(ctx context.Context, t *testing.T, p *wire.PacketIO, path, query string, column []byte) {
	const want = "<x>患者😀</x>\n"

	switch path {
	case testXMLText:
		if !testpeer.ReadQuery(ctx, t, p, query) {
			return
		}
	case testXMLServer:
		if !publictest.ServePrepareStatus(ctx, t, p, query, 52, 1, [][]byte{column}, 6) || !testpeer.ReadCommand(ctx, t, p, append(publictest.ExecuteHeader(52), 0, 1, 8, 0, 1, 0, 0, 0, 0, 0, 0, 0)) {
			return
		}
	default:
		payload, err := wire.EncodePrepareExecute(0, []byte(query), []wire.Parameter{publictest.LobIntFixture(1)}, 4096)
		if err != nil || !testpeer.ReadCommand(ctx, t, p, payload) || !testpeer.SendPackets(ctx, t, p, publictest.CombinedHeader(52, 1, 1, 0), testpeer.TextColumn("p", 8, 63, 0), []byte{254, 0, 0, 6, 0}) {
			return
		}
	}
	if path != testXMLCombined && !testpeer.SendPackets(ctx, t, p, []byte{1}) {
		return
	}
	row := append([]byte{byte(len(want))}, want...)
	null := []byte{251}
	if path != testXMLText {
		row = append([]byte{0, 0}, row...)
		null = []byte{0, 4}
	}
	if !testpeer.SendPackets(ctx, t, p, column, []byte{254, 0, 0, 6, 0}, row, null, []byte{254, 0, 0, 6, 0}) {
		return
	}
	if path == testXMLCombined && !testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, 6, 0, 0, 0}) {
		return
	}
	if path != testXMLText && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 52)) {
		return
	}
	if !testpeer.ServeOK(ctx, t, p, []byte{14}, 6) {
		return
	}
	p.ResetSequence()
	testpeer.ServeQuit(t, p)
}
