package oceanbase

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

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestColumnTypes(t *testing.T) {
	for _, tt := range []struct {
		name                     string
		mode                     Mode
		column                   wire.Column
		length, precision, scale int64
		lengthOK, decimalOK      bool
	}{
		{"utf8 characters", ModeMySQL, wire.Column{Type: 253, Charset: 45, Length: 40}, 10, 0, 0, true, false},
		{"binary bytes", ModeMySQL, wire.Column{Type: 252, Charset: 63, Length: 400}, 400, 0, 0, true, false},
		{"unknown collation", ModeMySQL, wire.Column{Type: 253, Charset: 999, Length: 40}, 0, 0, 0, false, false},
		{"nonintegral length", ModeMySQL, wire.Column{Type: 253, Charset: 45, Length: 41}, 0, 0, 0, false, false},
		{"decimal sign and dot", ModeMySQL, wire.Column{Type: 246, Length: 12, Decimals: 2}, 0, 10, 2, false, true},
		{"unsigned decimal", ModeMySQL, wire.Column{Type: 246, Length: 11, Decimals: 2, Flags: 32}, 0, 10, 2, false, true},
		{"unknown decimal", ModeMySQL, wire.Column{Type: 246, Decimals: 31}, 0, 0, 0, false, false},
		{"invalid decimal precision", ModeMySQL, wire.Column{Type: 246, Length: 100, Decimals: 2}, 0, 0, 0, false, false},
		{"invalid oracle scale", ModeOracle, wire.Column{Type: 246, Precision: 10, Decimals: 128}, 0, 0, 0, false, false},
		{"oracle float binary precision", ModeOracle, wire.Column{Type: 206, Precision: 24}, 0, 0, 0, false, false},
		{"oracle float scale marker", ModeOracle, wire.Column{Type: 206, Precision: 24, Decimals: 129}, 0, 0, 0, false, false},
		{"oracle negative scale", ModeOracle, wire.Column{Type: 246, Precision: 10, Decimals: 254}, 0, 10, -2, false, true},
		{"oracle unconstrained number", ModeOracle, wire.Column{Type: 246, Length: 40}, 0, 0, 0, false, false},
		{"oracle varchar characters", ModeOracle, wire.Column{Type: 253, Charset: 45, Length: 40}, 40, 0, 0, true, false},
		{"oracle nchar", ModeOracle, wire.Column{Type: 208, Charset: 54, Length: 40}, 10, 0, 0, true, false},
		{"integer no string length", ModeMySQL, wire.Column{Type: 3, Length: 11}, 0, 0, 0, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := &textRows{conn: &conn{session: &handshakeSession{mode: tt.mode}}, columns: []wire.Column{tt.column}}
			lengths, ok := any(r).(driver.RowsColumnTypeLength)
			if !ok {
				t.Fatal("RowsColumnTypeLength missing")
			}
			decimals, ok := any(r).(driver.RowsColumnTypePrecisionScale)
			if !ok {
				t.Fatal("RowsColumnTypePrecisionScale missing")
			}
			if n, known := lengths.ColumnTypeLength(0); n != tt.length || known != tt.lengthOK {
				t.Fatalf("length=%d,%v", n, known)
			}
			if p, s, known := decimals.ColumnTypePrecisionScale(0); p != tt.precision || s != tt.scale || known != tt.decimalOK {
				t.Fatalf("decimal=%d,%d,%v", p, s, known)
			}
		})
	}
}

func TestMetadataFilters(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for kind := MetadataCatalogs; kind <= MetadataServerInfo; kind++ {
			if mode == ModeMySQL && (kind == MetadataSynonyms || kind == MetadataTypes || kind == MetadataTypeAttributes || kind == MetadataCollectionElements) {
				continue
			}
			plan, err := metadataPlan(mode, "4.3.5.0", MetadataFilter{Kind: kind})
			if err != nil || len(plan.columns) == 0 || plan.query == "" {
				t.Fatalf("mode=%d kind=%d: %v", mode, kind, err)
			}
		}
		attack := "MiX_' OR 1=1 --"
		p, err := metadataPlan(mode, "4.3.5.0", MetadataFilter{Kind: MetadataColumns, SchemaPattern: attack, NamePattern: "a/_%", ColumnPattern: "X%"})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(p.query, attack) {
			t.Fatal("filter became SQL")
		}
		if reflect.DeepEqual(p.args, []driver.NamedValue{}) || len(p.args) != 3 || p.args[0].Value != attack || p.args[1].Value != "a/_%" || p.args[2].Value != "X%" {
			t.Fatalf("lost pattern/case: %#v", p.args)
		}
		if _, err = metadataPlan(mode, "4.3.5.0", MetadataFilter{Kind: MetadataKind(255)}); err == nil {
			t.Fatal("unknown kind accepted")
		}
		if _, err = metadataPlan(mode, "4.3.5.0", MetadataFilter{Kind: MetadataTables, TableTypes: []string{"TABLE'); DROP TABLE t;--"}}); err == nil {
			t.Fatal("unknown table type accepted")
		}
		if _, err = metadataPlan(mode, "4.3.5.0", MetadataFilter{Kind: MetadataTables, TableTypes: []string{"TABLE", "VIEW", "SYNONYM", "MATERIALIZED VIEW", "TABLE"}}); err == nil {
			t.Fatal("unbounded table-type list accepted")
		}
	}
	for _, version := range []string{"", "5.7.25", "OceanBase 3.2.4.0", "OceanBase_CE 4.0.0.0"} {
		if _, err := metadataPlan(ModeMySQL, version, MetadataFilter{Kind: MetadataRoutineParameters}); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unreliable parameter dictionary accepted %q: %v", version, err)
		}
	}
	var nilConn *sql.Conn
	for _, session := range []Session{nil, nilConn, new(sql.DB)} {
		if _, err := Metadata(context.Background(), session, MetadataFilter{Kind: MetadataTables}); err == nil {
			t.Fatal("unpinned session accepted")
		}
	}
}

func TestMetadataIndexOrdering(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		want := `SELECT i.TABLE_CATALOG AS "catalog",i.TABLE_SCHEMA AS "schema",i.TABLE_NAME AS "table_name",i.INDEX_NAME AS "name",i.NON_UNIQUE AS "non_unique",i.COLUMN_NAME AS "column_name",i.SEQ_IN_INDEX AS "ordinal_position",CASE i.COLLATION WHEN 'A' THEN 'ASC' WHEN 'D' THEN 'DESC' END AS "ordering",i.CARDINALITY AS "cardinality",i.INDEX_TYPE AS "type" FROM information_schema.STATISTICS i WHERE 1=1 ORDER BY i.TABLE_SCHEMA,i.TABLE_NAME,i.INDEX_NAME,i.SEQ_IN_INDEX`
		if !readQuery(ctx, t, p, want) {
			return
		}
		if !sendPackets(ctx, t, p, []byte{10}) {
			return
		}
		for i := 0; i < 10; i++ {
			if !sendPackets(ctx, t, p, textColumn("v", 253, 45, 0)) {
				return
			}
		}
		if !sendPackets(ctx, t, p, []byte{254, 0, 0, 2, 0}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	rows, err := Metadata(ctx, pinned, MetadataFilter{Kind: MetadataIndexes})
	if err != nil {
		t.Fatal(err)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMetadataPublicSession(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			query := `SELECT s.CATALOG_NAME AS "catalog",s.SCHEMA_NAME AS "schema" FROM information_schema.SCHEMATA s WHERE 1=1 AND s.SCHEMA_NAME LIKE  _utf8mb4 X'4d69585f25'  ESCAPE '/' ORDER BY s.SCHEMA_NAME`
			catalog := []byte{3, 'd', 'e', 'f'}
			if mode == ModeOracle {
				query = `SELECT NULL AS "catalog",u.USERNAME AS "schema" FROM ALL_USERS u WHERE 1=1 AND u.USERNAME LIKE  'MiX_%'  ESCAPE '/' ORDER BY u.USERNAME`
				catalog = []byte{251}
			}
			db := txDB(t, mode, func(ctx context.Context, p *wire.PacketIO) {
				if !readQuery(ctx, t, p, query) {
					return
				}
				row := append(catalog, 3, 'M', 'i', 'X')
				if !sendPackets(ctx, t, p, []byte{2}, textColumn("catalog", 253, 45, 0), textColumn("schema", 253, 45, 0), []byte{254, 0, 0, 2, 0}, row, []byte{254, 0, 0, 2, 0}) {
					return
				}
				if !serveOK(ctx, t, p, []byte{14}, 2) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			pinned, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = pinned.Close() }()
			rows, err := Metadata(ctx, pinned, MetadataFilter{Kind: MetadataSchemas, SchemaPattern: "MiX_%"})
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
			if err = rows.Scan(&cat, &schema); err != nil || schema != "MiX" || cat.Valid != (mode == ModeMySQL) {
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

func TestMetadataInternalBoundary(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !readQuery(ctx, t, p, "SELECT business") {
			return
		}
		if !sendPackets(ctx, t, p, []byte{1}, textColumn("v", 3, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{1, '1'}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		if !readQuery(ctx, t, p, `SELECT 'def' AS "catalog" FROM dual WHERE 1=1`) {
			return
		}
		if !sendPackets(ctx, t, p, []byte{1}, textColumn("catalog", 253, 45, 0), []byte{254, 0, 0, 2, 0}, []byte{3, 'd', 'e', 'f'}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		if !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	err = pinned.Raw(func(raw any) error {
		c := raw.(*conn)
		if err := c.lock(ctx); err != nil {
			return err
		}
		defer c.unlock()
		business, err := c.queryCommandOwnedLocked(ctx, []byte("\x03SELECT business"), false, nil, true)
		if err != nil {
			return err
		}
		if _, err = c.metadataLocked(ctx, MetadataFilter{Kind: MetadataCatalogs}); !errors.Is(err, errResponseActive) {
			return errors.New("unread response allowed an internal command")
		}
		if err = business.Close(); err != nil {
			return err
		}
		rows, err := c.metadataLocked(ctx, MetadataFilter{Kind: MetadataCatalogs})
		if err != nil {
			return err
		}
		values := make([]driver.Value, 1)
		if err = rows.Next(values); err != nil || values[0] != "def" {
			return errors.New("internal metadata values missing")
		}
		if err = rows.Close(); err != nil {
			return err
		}
		if len(c.mu) != 1 || c.responseActive {
			return errors.New("internal rows changed caller gate ownership")
		}
		return c.commandOK(ctx, []byte{14})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMetadataStatementParameters(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, "SELECT ?", 9, 1, nil) {
			return
		}
		if !readCommand(ctx, t, p, stmtPacket(25, 9)) {
			return
		}
		if !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	rows, err := Metadata(ctx, pinned, MetadataFilter{Kind: MetadataStatementParameters, Query: "SELECT ?"})
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

func TestMetadataOracleParameterIdentity(t *testing.T) {
	connector, err := NewConnector(Config{User: testUser, Mode: ModeOracle, PrepareMode: PrepareClient, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 6) || !serveBaseline(ctx, t, p, 6) {
			return
		}
		ctx = context.Background()
		if !readCommand(ctx, t, p, []byte("\x16SELECT ?,? FROM dual")) {
			return
		}
		header := binary.LittleEndian.AppendUint32([]byte{0}, 11)
		header = append(header, 0, 0, 2, 0, 0, 0, 0)
		parameter := textColumn("ignored", 246, 45, 1)
		parameter[len(parameter)-3] = 254
		parameter[len(parameter)-2] = 10
		parameter[len(parameter)-1] = 2
		if !sendPackets(ctx, t, p, header, parameter, parameter, []byte{254, 0, 0, 6, 0}) {
			return
		}
		if !readCommand(ctx, t, p, stmtPacket(25, 11)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
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
	rows, err := Metadata(ctx, pinned, MetadataFilter{Kind: MetadataStatementParameters, Query: "SELECT :value,:VALUE FROM dual"})
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

func TestMetadataDictionaryErrors(t *testing.T) {
	const malformedNumber = "malformed-number"
	const denied = "privilege"
	for _, failure := range []string{denied, "column-count", malformedNumber} {
		t.Run(failure, func(t *testing.T) {
			db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				p.ResetSequence()
				if _, err := p.ReadMessage(ctx); err != nil {
					t.Error(err)
					return
				}
				switch failure {
				case denied:
					if !sendPackets(ctx, t, p, []byte{255, 0x76, 0x04, '#', '4', '2', '0', '0', '0', 'd', 'e', 'n', 'i', 'e', 'd'}) {
						return
					}
				case "column-count":
					if !sendPackets(ctx, t, p, []byte{1}, textColumn("wrong", 253, 45, 0), []byte{254, 0, 0, 2, 0}, []byte{254, 0, 0, 2, 0}) {
						return
					}
				case malformedNumber:
					if !sendPackets(ctx, t, p, []byte{4}, textColumn("v", 253, 45, 0), textColumn("m", 253, 45, 0), textColumn("p", 253, 45, 0), textColumn("t", 3, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{1, 'v', 1, 'm', 1, 'x', 1, '0'}, []byte{254, 0, 0, 2, 0}) {
						return
					}
				}
				if !serveOK(ctx, t, p, []byte{14}, 2) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			pinned, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = pinned.Close() }()
			rows, err := Metadata(ctx, pinned, MetadataFilter{Kind: MetadataServerInfo})
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
				var server *Error
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

func TestMetadataTypeDictionaryProjection(t *testing.T) {
	for _, tt := range []struct {
		kind  MetadataKind
		query string
	}{
		{MetadataTypeAttributes, `SELECT NULL AS "catalog",a.OWNER AS "schema",a.TYPE_NAME AS "type_name",a.ATTR_NAME AS "name",a.ATTR_NO AS "ordinal_position",a.ATTR_TYPE_NAME AS "data_type",a.ATTR_TYPE_OWNER AS "type_owner",a.LENGTH AS "length",a.NUMBER_PRECISION AS "precision",a.SCALE AS "scale" FROM ALL_TYPE_ATTRS a WHERE 1=1 ORDER BY a.OWNER,a.TYPE_NAME,a.ATTR_NO`},
		{MetadataCollectionElements, `SELECT NULL AS "catalog",c.OWNER AS "schema",c.TYPE_NAME AS "name",c.COLL_TYPE AS "collection_type",c.UPPER_BOUND AS "upper_bound",c.ELEM_TYPE_NAME AS "element_type",c.ELEM_TYPE_OWNER AS "element_owner",c.LENGTH AS "length",c.NUMBER_PRECISION AS "precision",c.SCALE AS "scale" FROM ALL_COLL_TYPES c WHERE 1=1 ORDER BY c.OWNER,c.TYPE_NAME`},
	} {
		t.Run(fmt.Sprint(tt.kind), func(t *testing.T) {
			db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
				if !readQuery(ctx, t, p, tt.query) {
					return
				}
				if !sendPackets(ctx, t, p, []byte{10}) {
					return
				}
				for i := 0; i < 10; i++ {
					if !sendPackets(ctx, t, p, textColumn("v", 253, 45, 0)) {
						return
					}
				}
				if !sendPackets(ctx, t, p, []byte{254, 0, 0, 6, 0}, []byte{254, 0, 0, 6, 0}) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			pinned, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = pinned.Close() }()
			rows, err := Metadata(ctx, pinned, MetadataFilter{Kind: tt.kind})
			if err != nil {
				t.Fatal(err)
			}
			if err = rows.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMetadataTransaction(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}}) {
			return
		}
		if !readQuery(ctx, t, p, `SELECT 'def' AS "catalog" FROM dual WHERE 1=1`) {
			return
		}
		if !sendPackets(ctx, t, p, []byte{1}, textColumn("catalog", 253, 45, 0), []byte{254, 0, 0, 3, 0}, []byte{3, 'd', 'e', 'f'}, []byte{254, 0, 0, 3, 0}) {
			return
		}
		if !serveTxSteps(ctx, t, p, []txStep{{testRollbackSQL, 2}}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := Metadata(ctx, tx, MetadataFilter{Kind: MetadataCatalogs})
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
