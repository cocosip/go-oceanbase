package catalog_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/rowset"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestMetadataTransaction(t *testing.T) {
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: testMySQLReadWrite, Status: 3}}) {
			return
		}
		if !testpeer.ReadQuery(ctx, t, p, `SELECT 'def' AS "catalog" FROM dual WHERE 1=1`) {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("catalog", 253, 45, 0), []byte{254, 0, 0, 3, 0}, []byte{3, 'd', 'e', 'f'}, []byte{254, 0, 0, 3, 0}) {
			return
		}
		if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: testRollbackSQL, Status: 2}}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := oceanbase.Metadata(ctx, tx, oceanbase.MetadataFilter{Kind: oceanbase.MetadataCatalogs})
	if err != nil {
		t.Fatal(err)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestMetadataTypeDictionaryProjection(t *testing.T) {
	for _, tt := range []struct {
		kind  oceanbase.MetadataKind
		query string
	}{
		{oceanbase.MetadataTypeAttributes, `SELECT NULL AS "catalog",a.OWNER AS "schema",a.TYPE_NAME AS "type_name",a.ATTR_NAME AS "name",a.ATTR_NO AS "ordinal_position",a.ATTR_TYPE_NAME AS "data_type",a.ATTR_TYPE_OWNER AS "type_owner",a.LENGTH AS "length",a.NUMBER_PRECISION AS "precision",a.SCALE AS "scale" FROM ALL_TYPE_ATTRS a WHERE 1=1 ORDER BY a.OWNER,a.TYPE_NAME,a.ATTR_NO`},
		{oceanbase.MetadataCollectionElements, `SELECT NULL AS "catalog",c.OWNER AS "schema",c.TYPE_NAME AS "name",c.COLL_TYPE AS "collection_type",c.UPPER_BOUND AS "upper_bound",c.ELEM_TYPE_NAME AS "element_type",c.ELEM_TYPE_OWNER AS "element_owner",c.LENGTH AS "length",c.NUMBER_PRECISION AS "precision",c.SCALE AS "scale" FROM ALL_COLL_TYPES c WHERE 1=1 ORDER BY c.OWNER,c.TYPE_NAME`},
	} {
		t.Run(fmt.Sprint(tt.kind), func(t *testing.T) {
			db := publictest.TxDB(t, oceanbase.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ReadQuery(ctx, t, p, tt.query) {
					return
				}
				if !testpeer.SendPackets(ctx, t, p, []byte{10}) {
					return
				}
				for i := 0; i < 10; i++ {
					if !testpeer.SendPackets(ctx, t, p, testpeer.TextColumn("v", 253, 45, 0)) {
						return
					}
				}
				if !testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, 6, 0}, []byte{254, 0, 0, 6, 0}) {
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
			rows, err := oceanbase.Metadata(ctx, pinned, oceanbase.MetadataFilter{Kind: tt.kind})
			if err != nil {
				t.Fatal(err)
			}
			if err = rows.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMetadataDictionaryErrors(t *testing.T) {
	const malformedNumber = "malformed-number"
	const denied = "privilege"
	for _, failure := range []string{denied, "column-count", malformedNumber} {
		t.Run(failure, func(t *testing.T) {
			db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				p.ResetSequence()
				if _, err := p.ReadMessage(ctx); err != nil {
					t.Error(err)
					return
				}
				switch failure {
				case denied:
					if !testpeer.SendPackets(ctx, t, p, []byte{255, 0x76, 0x04, '#', '4', '2', '0', '0', '0', 'd', 'e', 'n', 'i', 'e', 'd'}) {
						return
					}
				case "column-count":
					if !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("wrong", 253, 45, 0), []byte{254, 0, 0, 2, 0}, []byte{254, 0, 0, 2, 0}) {
						return
					}
				case malformedNumber:
					if !testpeer.SendPackets(ctx, t, p, []byte{4}, testpeer.TextColumn("v", 253, 45, 0), testpeer.TextColumn("m", 253, 45, 0), testpeer.TextColumn("p", 253, 45, 0), testpeer.TextColumn("t", 3, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{1, 'v', 1, 'm', 1, 'x', 1, '0'}, []byte{254, 0, 0, 2, 0}) {
						return
					}
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
			rows, err := oceanbase.Metadata(ctx, pinned, oceanbase.MetadataFilter{Kind: oceanbase.MetadataServerInfo})
			if failure == malformedNumber {
				if err != nil {
					t.Fatal(err)
				}
				if rows.Next() || rows.Err() == nil {
					t.Fatal("invalid numeric metadata accepted")
				}
				_ = rows.Close()
			} else if err == nil {
				_ = rows.Close()
				t.Fatal("dictionary failure hidden")
			}
			if failure == denied {
				var server *oceanbase.Error
				if !errors.As(err, &server) || server.Code != 1142 {
					t.Fatal(err)
				}
			}
			if err = pinned.PingContext(ctx); err != nil {
				t.Fatal("complete dictionary error poisoned session", err)
			}
		})
	}
}

func TestMetadataServerIdentity(t *testing.T) {
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			name, types := "MYSQL", byte('0')
			if mode == oceanbase.ModeOracle {
				name, types = "ORACLE", '1'
			}
			query := `SELECT ob_version() AS "oceanbase_version",'` + name + `' AS "compatibility_mode",1 AS "parameter_dictionary",` + string(types) + ` AS "type_dictionary" FROM dual WHERE 1=1`
			db := publictest.TxDB(t, mode, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ReadQuery(ctx, t, p, query) {
					return
				}
				row := append([]byte{7}, "4.3.5.0"...)
				row = append(append(row, byte(len(name))), name...)
				row = append(row, 1, '1', 1, types)
				if !testpeer.SendPackets(ctx, t, p, []byte{4}, testpeer.TextColumn("v", 253, 45, 0), testpeer.TextColumn("m", 253, 45, 0), testpeer.TextColumn("p", 3, 63, 0), testpeer.TextColumn("t", 3, 63, 0), []byte{254, 0, 0, 2, 0}, row, []byte{254, 0, 0, 2, 0}) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			rows, err := oceanbase.Metadata(context.Background(), publictest.PinnedSession(t, db), oceanbase.MetadataFilter{Kind: oceanbase.MetadataServerInfo})
			if err != nil {
				t.Fatal(err)
			}
			labels, err := rows.Columns()
			if err != nil || strings.Join(labels, " ") != "oceanbase_version compatibility_mode parameter_dictionary type_dictionary" {
				t.Fatal(labels, err)
			}
			if !rows.Next() {
				t.Fatal(rows.Err())
			}
			var version, actualMode string
			var parameters, typeDictionary int
			if err = rows.Scan(&version, &actualMode, &parameters, &typeDictionary); err != nil || version != "4.3.5.0" || actualMode != name || parameters != 1 || typeDictionary != int(types-'0') {
				t.Fatal(version, actualMode, parameters, typeDictionary, err)
			}
			if err = rows.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMetadataOracleParameterIdentity(t *testing.T) {
	connector, err := oceanbase.NewConnector(oceanbase.Config{User: testUser, Mode: oceanbase.ModeOracle, PrepareMode: oceanbase.PrepareClient, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeConnect(ctx, t, p, 6) || !testpeer.ServeBaseline(ctx, t, p, 6) {
			return
		}
		ctx = context.Background()
		if !testpeer.ReadCommand(ctx, t, p, []byte("\x16SELECT ?,? FROM dual")) {
			return
		}
		header := binary.LittleEndian.AppendUint32([]byte{0}, 11)
		header = append(header, 0, 0, 2, 0, 0, 0, 0)
		parameter := testpeer.TextColumn("ignored", 246, 45, 1)
		parameter[len(parameter)-3] = 254
		parameter[len(parameter)-2] = 10
		parameter[len(parameter)-1] = 2
		if !testpeer.SendPackets(ctx, t, p, header, parameter, parameter, []byte{254, 0, 0, 6, 0}) {
			return
		}
		if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 11)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})})
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	rows, err := oceanbase.Metadata(ctx, pinned, oceanbase.MetadataFilter{Kind: oceanbase.MetadataStatementParameters, Query: "SELECT :value,:VALUE FROM dual"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for i := 1; i <= 2; i++ {
		if !rows.Next() {
			t.Fatal(rows.Err())
		}
		var physical, logical, nullable, precision, scale, typ, directionCode int64
		var name, dataType string
		var direction sql.NullString
		var length sql.NullInt64
		if err = rows.Scan(&physical, &logical, &name, &direction, &dataType, &nullable, &length, &precision, &scale, &typ, &directionCode); err != nil {
			t.Fatal(err)
		}
		if physical != int64(i) || logical != 1 || name != "VALUE" || direction.Valid || dataType != "NUMBER" || nullable != 0 || length.Valid || precision != 10 || scale != -2 || typ != 246 || directionCode != 2 {
			t.Fatalf("parameter identity/description lost: %d %d %q %q %d %d %d %d %d", physical, logical, name, dataType, nullable, precision, scale, typ, directionCode)
		}
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal(rows.Err())
	}
}

func TestMetadataInternalBoundary(t *testing.T) {
	op := catalogOperation(t, config.Config{}, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadQuery(ctx, t, p, "SELECT business") {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 3, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{1, '1'}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		if !testpeer.ReadQuery(ctx, t, p, `SELECT 'def' AS "catalog" FROM dual WHERE 1=1`) {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("catalog", 253, 45, 0), []byte{254, 0, 0, 2, 0}, []byte{3, 'd', 'e', 'f'}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		if !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	business, err := command.Start(ctx, op, []byte("\x03SELECT business"), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = business.Advance(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = catalog.Metadata(ctx, op, oceanbase.MetadataFilter{Kind: oceanbase.MetadataCatalogs}, false); !errors.Is(err, catalog.ErrResponseActive) {
		t.Fatal("unread response allowed an internal command", err)
	}
	if err = business.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := catalog.Metadata(ctx, op, oceanbase.MetadataFilter{Kind: oceanbase.MetadataCatalogs}, false)
	if err != nil {
		t.Fatal(err)
	}
	rows := rowset.New(ctx, config.ModeMySQL, false, []rowset.Set{{Columns: result.Columns, Rows: result.Rows}})
	values := make([]driver.Value, 1)
	if err = rows.Next(values); err != nil || values[0] != "def" {
		t.Fatal("internal metadata values missing", err)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if op.ResponseActive() {
		t.Fatal("internal rows changed caller gate ownership")
	}
	if _, err = command.ExecOK(ctx, op, []byte{14}); err != nil {
		t.Fatal(err)
	}
	op.Packets().ResetSequence()
	if err = op.Packets().WriteMessage(ctx, []byte{1}); err != nil {
		t.Fatal(err)
	}
}

func TestMetadataPublicSession(t *testing.T) {
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			query := `SELECT s.CATALOG_NAME AS "catalog",s.SCHEMA_NAME AS "schema" FROM information_schema.SCHEMATA s WHERE 1=1 AND s.SCHEMA_NAME LIKE  _utf8mb4 X'4d69585f25'  ESCAPE '/' ORDER BY s.SCHEMA_NAME`
			catalog := []byte{3, 'd', 'e', 'f'}
			if mode == oceanbase.ModeOracle {
				query = `SELECT NULL AS "catalog",u.USERNAME AS "schema" FROM ALL_USERS u WHERE 1=1 AND u.USERNAME LIKE  'MiX_%'  ESCAPE '/' ORDER BY u.USERNAME`
				catalog = []byte{251}
			}
			db := publictest.TxDB(t, mode, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ReadQuery(ctx, t, p, query) {
					return
				}
				row := append(catalog, 3, 'M', 'i', 'X')
				if !testpeer.SendPackets(ctx, t, p, []byte{2}, testpeer.TextColumn("catalog", 253, 45, 0), testpeer.TextColumn("schema", 253, 45, 0), []byte{254, 0, 0, 2, 0}, row, []byte{254, 0, 0, 2, 0}) {
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
			rows, err := oceanbase.Metadata(ctx, pinned, oceanbase.MetadataFilter{Kind: oceanbase.MetadataSchemas, SchemaPattern: "MiX_%"})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rows.Close() }()
			columns, err := rows.ColumnTypes()
			if err != nil || len(columns) != 2 || columns[1].Name() != "schema" || columns[1].ScanType() != reflect.TypeFor[string]() {
				t.Fatal(columns, err)
			}
			if !rows.Next() {
				t.Fatal(rows.Err())
			}
			var cat sql.NullString
			var schema string
			if err = rows.Scan(&cat, &schema); err != nil || schema != "MiX" || cat.Valid != (mode == oceanbase.ModeMySQL) {
				t.Fatal(cat, schema, err)
			}
			if rows.Next() || rows.Err() != nil {
				t.Fatal(rows.Err())
			}
			if err = pinned.PingContext(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMetadataIndexOrdering(t *testing.T) {
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		want := `SELECT i.TABLE_CATALOG AS "catalog",i.TABLE_SCHEMA AS "schema",i.TABLE_NAME AS "table_name",i.INDEX_NAME AS "name",i.NON_UNIQUE AS "non_unique",i.COLUMN_NAME AS "column_name",i.SEQ_IN_INDEX AS "ordinal_position",CASE i.COLLATION WHEN 'A' THEN 'ASC' WHEN 'D' THEN 'DESC' END AS "ordering",i.CARDINALITY AS "cardinality",i.INDEX_TYPE AS "type" FROM information_schema.STATISTICS i WHERE 1=1 ORDER BY i.TABLE_SCHEMA,i.TABLE_NAME,i.INDEX_NAME,i.SEQ_IN_INDEX`
		if !testpeer.ReadQuery(ctx, t, p, want) {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{10}) {
			return
		}
		for i := 0; i < 10; i++ {
			if !testpeer.SendPackets(ctx, t, p, testpeer.TextColumn("v", 253, 45, 0)) {
				return
			}
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, 2, 0}, []byte{254, 0, 0, 2, 0}) {
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
	rows, err := oceanbase.Metadata(ctx, pinned, oceanbase.MetadataFilter{Kind: oceanbase.MetadataIndexes})
	if err != nil {
		t.Fatal(err)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
}
