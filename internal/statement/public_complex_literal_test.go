package statement_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/metadata"
)

func TestNamedComplexClientSQL(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		for _, collection := range []bool{false, true} {
			t.Run(fmt.Sprintf("prepared=%t/collection=%t", prepared, collection), func(t *testing.T) {
				const query = "INSERT INTO t VALUES (:v); INSERT INTO t VALUES (:v)"
				var v any = oceanbase.Object{Schema: "S", Type: "ROOT", Fields: []oceanbase.Attribute{{Name: "C", Value: oceanbase.Object{Type: "T", Fields: []oceanbase.Attribute{{Name: "N", Value: oceanbase.Number("1e2")}, {Name: "S", Value: "患者😀'"}}}}, {Name: "TAG"}}}
				literal := `"S"."ROOT"("S"."T"(100,UNISTR('\60A3\8005\D83D\DE00\0027')),NULL)`
				if collection {
					v = oceanbase.Array{Schema: "S", Type: "TA", Elements: []any{oceanbase.Number("7"), nil, int64(-9)}}
					literal = `"S"."TA"(7,NULL,-9)`
				}
				db := publictest.OutOracleDB(t, config.PrepareClient, func(ctx context.Context, p *wire.PacketIO) {
					if !serveConstructorDictionary(ctx, t, p, collection) {
						return
					}
					want := "INSERT INTO t VALUES ( " + literal + " ); INSERT INTO t VALUES ( " + literal + " )"
					if !testpeer.ReadQuery(ctx, t, p, want) || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, 14), publictest.CommandPacket(1, 0, 6)) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 6) {
						return
					}
					p.ResetSequence()
					testpeer.ServeQuit(t, p)
				}, func(cfg *config.Config) { cfg.AllowMultiStatements = true })
				defer func() { _ = db.Close() }()
				var result sql.Result
				var err error
				if prepared {
					stmt, e := db.Prepare(query)
					if e != nil {
						t.Fatal(e)
					}
					defer func() { _ = stmt.Close() }()
					result, err = stmt.Exec(sql.Named("v", v))
				} else {
					result, err = db.Exec(query, sql.Named("v", v))
				}
				if err != nil {
					t.Fatal(err)
				}
				if affected, err := result.RowsAffected(); err != nil || affected != 1 {
					t.Fatal(affected, err)
				}
				if err := db.Ping(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func serveConstructorDictionary(ctx context.Context, t *testing.T, p *wire.PacketIO, collection bool) bool {
	if collection {
		return publictest.ServeDescriptorRows(ctx, t, p, metadata.MetadataTypes, "S", "TA", [][]string{{"", "S", "TA", "COLLECTION", "0", "1", "1"}}) && publictest.ServeDescriptorRows(ctx, t, p, metadata.MetadataCollectionElements, "S", "TA", [][]string{{"", "S", "TA", "TABLE", "", "NUMBER", "", "", "", ""}})
	}
	return publictest.ServeComplexDictionaryFields(ctx, t, p, "ROOT", []oceanbase.Attribute{{Name: "C", Type: "T"}, {Name: "TAG", Type: valueformat.TypeNameNumber}}) && publictest.ServeComplexDictionaryFields(ctx, t, p, "T", []oceanbase.Attribute{{Name: "N", Type: valueformat.TypeNameNumber}, {Name: "S", Type: valueformat.TypeNameNVarchar2}})
}
