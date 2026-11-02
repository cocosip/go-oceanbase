package materialize_test

import (
	"context"
	"errors"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestNamedObjectTextSQLResults(t *testing.T) {
	for _, client := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "client"}[client], func(t *testing.T) {
			const query = "SELECT v,x FROM t WHERE id= 1 "
			xml := append(testpeer.TextColumn("x", 160, 63, 0), 0, 7, 'X', 'M', 'L', 'T', 'Y', 'P', 'E', 251)
			row := wire.AppendComplexLenBytes(nil, []byte("T(SUB(7, '患者😀, (c) 'q''), NULL)"))
			row = wire.AppendComplexLenBytes(row, []byte("<x/>"))
			db := publictest.OutOracleDB(t, config.PrepareClient, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ReadQuery(ctx, t, p, query) || !testpeer.SendPackets(ctx, t, p, []byte{2}, complexColumnFixture(2), xml, []byte{254, 0, 0, 6, 0}, row, []byte{251, 251}, []byte{254, 0, 0, 14, 0}, []byte{1}, testpeer.TextColumn("tail", 253, 45, 0), []byte{254, 0, 0, 6, 0}, []byte{4, 't', 'a', 'i', 'l'}, []byte{254, 0, 0, 6, 0}) {
					return
				}
				if !publictest.ServeComplexDictionaryFields(ctx, t, p, "T", []oceanbase.Attribute{{Name: "C", Type: "SUB"}, {Name: "TAG", Type: valueformat.TypeNameNumber}}) || !publictest.ServeComplexDictionaryFields(ctx, t, p, "SUB", []oceanbase.Attribute{{Name: "N", Type: valueformat.TypeNameNumber}, {Name: "S", Type: valueformat.TypeNameNVarchar2}}) || !testpeer.ReadCommand(ctx, t, p, []byte{14}) || !testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, 6, 0, 0, 0}) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			sqlText, args := query, []any(nil)
			if client {
				sqlText, args = "SELECT v,x FROM t WHERE id=?", []any{1}
			}
			rows, err := db.Query(sqlText, args...)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rows.Close() }()
			var o oceanbase.Object
			var x *string
			if !rows.Next() {
				t.Fatal(rows.Err())
			}
			if err := rows.Scan(&o, &x); err != nil || o.Type != "T" || x == nil || *x != "<x/>" {
				t.Fatal(o, x, err)
			}
			child := o.Fields[0].Value.(oceanbase.Object)
			if child.Fields[0].Value != oceanbase.Number("7") || child.Fields[1].Value != "患者😀, (c) 'q'" || o.Fields[1].Value != nil {
				t.Fatal(o)
			}
			if !rows.Next() {
				t.Fatal(rows.Err())
			}
			if err := rows.Scan(&o, &x); err != nil || !o.Null || x != nil {
				t.Fatal(o, x, err)
			}
			if rows.Next() || !rows.NextResultSet() || !rows.Next() {
				t.Fatal(rows.Err())
			}
			var tail string
			if err := rows.Scan(&tail); err != nil || tail != "tail" {
				t.Fatal(tail, err)
			}
			if err := rows.Close(); err != nil {
				t.Fatal(err)
			}
			if err := db.Ping(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTextObjectNullStillValidatesDescriptor(t *testing.T) {
	db := publictest.OutOracleDB(t, config.PrepareClient, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadQuery(ctx, t, p, "SELECT v FROM t") || !testpeer.SendPackets(ctx, t, p, []byte{1}, complexColumnFixture(2), []byte{254, 0, 0, 6, 0}, []byte{251}, []byte{254, 0, 0, 6, 0}) || !publictest.ServeComplexDictionaryFields(ctx, t, p, "T", []oceanbase.Attribute{{Name: "R", Type: "RAW"}}) {
			return
		}
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("unsupported text descriptor connection reused")
		}
	})
	defer func() { _ = db.Close() }()
	rows, err := db.Query("SELECT v FROM t")
	if rows != nil {
		_ = rows.Close()
	}
	if !errors.Is(err, dberrors.ErrUnsupported) {
		t.Fatal(err)
	}
}
