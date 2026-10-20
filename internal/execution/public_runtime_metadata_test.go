package execution_test

import (
	context "context"
	sql "database/sql"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestMetadataStatementParameters(t *testing.T) {
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, "SELECT ?", 9, 1, nil) {
			return
		}
		if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 9)) {
			return
		}
		if !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	rows, err := oceanbase.Metadata(ctx, pinned, oceanbase.MetadataFilter{Kind: oceanbase.MetadataStatementParameters, Query: "SELECT ?"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		t.Fatal(rows.Err())
	}
	var physical, logical, typ, directionCode int64
	var name, direction, dataType sql.NullString
	var nullable, length, precision, scale sql.NullInt64
	if err = rows.Scan(&physical, &logical, &name, &direction, &dataType, &nullable, &length, &precision, &scale, &typ, &directionCode); err != nil {
		t.Fatal(err)
	}
	if physical != 1 || logical != 1 || name.Valid || direction.Valid || nullable.Valid || typ != 253 || directionCode != 0 || dataType.String != "VARCHAR" || length.Valid || precision.Valid || scale.Valid {
		t.Fatalf("parameter metadata fabricated or lost: %d %d %#v %#v %#v %#v %#v", physical, logical, name, direction, dataType, nullable, length)
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	if err = pinned.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
}
