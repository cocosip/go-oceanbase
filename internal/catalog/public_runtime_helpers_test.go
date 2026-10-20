package catalog_test

import (
	context "context"
	sql "database/sql"
	hex "encoding/hex"
	fmt "fmt"
	reflect "reflect"
	strings "strings"
	atomic "sync/atomic"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	catalog "github.com/cocosip/go-oceanbase/internal/catalog"
	command "github.com/cocosip/go-oceanbase/internal/command"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	valuecodec "github.com/cocosip/go-oceanbase/internal/valuecodec"
	valueformat "github.com/cocosip/go-oceanbase/internal/valueformat"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

const (
	testAutocommitOn    = "SET autocommit=1"
	testMySQLReadWrite  = "START TRANSACTION READ WRITE"
	testSerializableSQL = "SET TRANSACTION ISOLATION LEVEL SERIALIZABLE"
	testAutocommitOff   = "SET autocommit=0"
	testCommitSQL       = "COMMIT"
	testRollbackSQL     = "ROLLBACK"
	testOracleReadWrite = "SET TRANSACTION READ WRITE"
)

const testUser = "app"

const testRoutineOldVersion = "3.2.4.0"

type metadataContractFixture struct {
	kind    oceanbase.MetadataKind
	query   string
	columns string
	numeric map[int]bool
	rows    [][]any
}

const localFilenameCharset = "gbk"

const (
	testProxyCapability = "ob_capability_flag"
	testOB20            = "ob20"
	testCompression     = "compression"
	testStandard        = "standard"
)

func testMetadataCatalogAndRemarks(t *testing.T, mode oceanbase.Mode, charset, framing string) {
	t.Helper()
	fixtures := metadataContractFixtures(mode)
	catalogs := []string{"", executionColumnCatalog, "d%", "x' OR 1=1 --", "库/_%"}
	var sessions atomic.Int32
	done := make(chan struct{})
	db := publictest.BatchRewriteDB(t, oceanbase.Config{Mode: mode, Session: oceanbase.SessionOptions{Charset: charset}}, framing, func(ctx context.Context, p *wire.PacketIO) {
		if sessions.Add(1) != 1 {
			t.Error("metadata opened another physical session")
			return
		}
		defer close(done)
		serveMetadataContract(ctx, t, p, mode, charset, fixtures, catalogs)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pinned := publictest.PinnedSession(t, db)
	defer func() { _ = pinned.Close() }()
	for _, fixture := range fixtures {
		for _, catalog := range catalogs {
			want := fixture.rows
			if catalog != "" && (mode == oceanbase.ModeOracle || catalog != executionColumnCatalog) {
				want = nil
			}
			assertMetadataContract(ctx, t, pinned, fixture, catalog, want)
		}
	}
	for _, fixture := range refreshedMetadataRemarks(fixtures) {
		assertMetadataContract(ctx, t, pinned, fixture, "", fixture.rows)
	}
	if err := pinned.PingContext(ctx); err != nil {
		t.Fatal("metadata response disturbed the original session", err)
	}
	if err := pinned.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("metadata peer did not finish", ctx.Err())
	}
	if sessions.Load() != 1 || db.Stats().OpenConnections != 0 {
		t.Fatal("metadata connection ownership changed")
	}
}

func assertMetadataContract(ctx context.Context, t *testing.T, pinned *sql.Conn, fixture metadataContractFixture, catalog string, want [][]any) {
	t.Helper()
	rows, err := oceanbase.Metadata(ctx, pinned, oceanbase.MetadataFilter{Kind: fixture.kind, Catalog: catalog})
	if err != nil {
		t.Fatal("metadata query failed", fixture.kind, catalog, err)
	}
	defer func() { _ = rows.Close() }()
	names, err := rows.Columns()
	if err != nil || !reflect.DeepEqual(names, strings.Fields(fixture.columns)) {
		t.Fatal("metadata schema changed", names, err)
	}
	columns, err := rows.ColumnTypes()
	if err != nil || len(columns) != len(names) {
		t.Fatal("metadata column types missing", err)
	}
	for i, column := range columns {
		name, scan := valueformat.TypeNameVarchar, reflect.TypeFor[string]()
		if fixture.numeric[i] {
			name, scan = valueformat.TypeNameBigInt, reflect.TypeFor[int64]()
		}
		nullable, known := column.Nullable()
		if column.DatabaseTypeName() != name || column.ScanType() != scan || !nullable || !known {
			t.Errorf("metadata column %d has inconsistent type or nullability", i)
		}
	}
	var got [][]any
	for rows.Next() {
		values := make([]any, len(names))
		dest := make([]any, len(names))
		for i := range dest {
			dest[i] = &values[i]
		}
		if err = rows.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		got = append(got, values)
	}
	if rows.Err() != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("metadata values changed for kind %d/catalog %q: got %#v, want %#v, error %v", fixture.kind, catalog, got, want, rows.Err())
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
}

func metadataContractFixtures(mode oceanbase.Mode) []metadataContractFixture {
	var catalog any = executionColumnCatalog
	var emptyRemark any = ""
	catalogQuery := `SELECT 'def' AS "catalog" FROM dual WHERE 1=1`
	schemaQuery := `SELECT s.CATALOG_NAME AS "catalog",s.SCHEMA_NAME AS "schema" FROM information_schema.SCHEMATA s WHERE 1=1 ORDER BY s.SCHEMA_NAME`
	tableQuery := `SELECT t.TABLE_CATALOG AS "catalog",t.TABLE_SCHEMA AS "schema",t.TABLE_NAME AS "name",CASE t.TABLE_TYPE WHEN 'BASE TABLE' THEN 'TABLE' WHEN 'SYSTEM VIEW' THEN 'VIEW' ELSE t.TABLE_TYPE END AS "type",t.TABLE_COMMENT AS "remarks" FROM information_schema.TABLES t WHERE 1=1 ORDER BY t.TABLE_SCHEMA,t.TABLE_NAME`
	columnQuery := `SELECT c.TABLE_CATALOG AS "catalog",c.TABLE_SCHEMA AS "schema",c.TABLE_NAME AS "table_name",c.COLUMN_NAME AS "name",c.ORDINAL_POSITION AS "ordinal_position",c.DATA_TYPE AS "data_type",c.CHARACTER_MAXIMUM_LENGTH AS "length",c.NUMERIC_PRECISION AS "precision",c.NUMERIC_SCALE AS "scale",CASE c.IS_NULLABLE WHEN 'YES' THEN 1 WHEN 'NO' THEN 0 END AS "nullable",c.COLUMN_DEFAULT AS "default_value",c.COLUMN_COMMENT AS "remarks",NULL AS "type_owner" FROM information_schema.COLUMNS c WHERE 1=1 ORDER BY c.TABLE_SCHEMA,c.TABLE_NAME,c.ORDINAL_POSITION`
	if mode == oceanbase.ModeOracle {
		catalog, emptyRemark = nil, nil
		catalogQuery = `SELECT CAST(NULL AS VARCHAR2(128)) AS "catalog" FROM dual WHERE 1=1 AND (1=0)`
		schemaQuery = `SELECT NULL AS "catalog",u.USERNAME AS "schema" FROM ALL_USERS u WHERE 1=1 ORDER BY u.USERNAME`
		tableQuery = `SELECT NULL AS "catalog",o.OWNER AS "schema",o.OBJECT_NAME AS "name",o.OBJECT_TYPE AS "type",c.COMMENTS AS "remarks" FROM ALL_OBJECTS o LEFT JOIN ALL_TAB_COMMENTS c ON c.OWNER=o.OWNER AND c.TABLE_NAME=o.OBJECT_NAME AND c.TABLE_TYPE=o.OBJECT_TYPE WHERE 1=1 AND (o.OBJECT_TYPE IN ('TABLE','VIEW','SYNONYM','MATERIALIZED VIEW') AND o.SUBOBJECT_NAME IS NULL) ORDER BY o.OWNER,o.OBJECT_NAME,o.OBJECT_TYPE`
		columnQuery = `SELECT NULL AS "catalog",c.OWNER AS "schema",c.TABLE_NAME AS "table_name",c.COLUMN_NAME AS "name",c.COLUMN_ID AS "ordinal_position",c.DATA_TYPE AS "data_type",CASE WHEN c.CHAR_USED='C' THEN c.CHAR_LENGTH ELSE c.DATA_LENGTH END AS "length",c.DATA_PRECISION AS "precision",c.DATA_SCALE AS "scale",CASE c.NULLABLE WHEN 'Y' THEN 1 WHEN 'N' THEN 0 END AS "nullable",c.DATA_DEFAULT AS "default_value",m.COMMENTS AS "remarks",c.DATA_TYPE_OWNER AS "type_owner" FROM ALL_TAB_COLUMNS c LEFT JOIN ALL_COL_COMMENTS m ON m.OWNER=c.OWNER AND m.TABLE_NAME=c.TABLE_NAME AND m.COLUMN_NAME=c.COLUMN_NAME WHERE 1=1 ORDER BY c.OWNER,c.TABLE_NAME,c.COLUMN_ID`
	}
	if mode == oceanbase.ModeOracle {
		columnQuery = strings.Replace(columnQuery, ` FROM ALL_TAB_COLUMNS c`, `,NULL AS "auto_increment",CASE c.VIRTUAL_COLUMN WHEN 'YES' THEN 1 WHEN 'NO' THEN 0 END AS "generated" FROM ALL_TAB_COLS c`, 1)
		columnQuery = strings.Replace(columnQuery, "WHERE 1=1 ORDER", "WHERE 1=1 AND (c.HIDDEN_COLUMN='NO') ORDER", 1)
		columnQuery = strings.Replace(columnQuery, "c.COLUMN_ID AS", "c.ORDINAL_POSITION AS", 1)
		columnQuery = strings.Replace(columnQuery, "FROM ALL_TAB_COLS c", "FROM (SELECT a.*,ROW_NUMBER() OVER(PARTITION BY a.OWNER,a.TABLE_NAME ORDER BY a.COLUMN_ID) AS ORDINAL_POSITION FROM ALL_TAB_COLS a WHERE a.HIDDEN_COLUMN='NO') c", 1)
	} else {
		columnQuery = strings.Replace(columnQuery, `c.NUMERIC_SCALE AS "scale"`, `CASE WHEN c.DATA_TYPE IN ('time','datetime','timestamp') THEN c.DATETIME_PRECISION ELSE c.NUMERIC_SCALE END AS "scale"`, 1)
		columnQuery = strings.Replace(columnQuery, ` FROM information_schema.COLUMNS c`, `,CASE WHEN c.EXTRA IS NULL THEN NULL WHEN LOWER(c.EXTRA) LIKE '%auto_increment%' THEN 1 ELSE 0 END AS "auto_increment",CASE WHEN c.EXTRA IS NULL THEN NULL WHEN UPPER(c.EXTRA) IN ('VIRTUAL','PERSISTENT') OR UPPER(c.EXTRA) LIKE '%VIRTUAL GENERATED%' OR UPPER(c.EXTRA) LIKE '%STORED GENERATED%' THEN 1 ELSE 0 END AS "generated" FROM information_schema.COLUMNS c`, 1)
	}
	var catalogRows [][]any
	if mode == oceanbase.ModeMySQL {
		catalogRows = [][]any{{executionColumnCatalog}}
	}
	return []metadataContractFixture{
		{oceanbase.MetadataCatalogs, catalogQuery, "catalog", nil, catalogRows},
		{oceanbase.MetadataSchemas, schemaQuery, "catalog schema", nil, [][]any{{catalog, metadataContractSchema}, {catalog, metadataContractOther}}},
		{oceanbase.MetadataTables, tableQuery, "catalog schema name type remarks", nil, [][]any{
			{catalog, metadataContractSchema, metadataContractName, metadataContractTable, metadataContractRemark},
			{catalog, metadataContractOther, metadataContractName, metadataContractView, emptyRemark},
			{catalog, "Third", "unannotated", metadataContractTable, nil},
		}},
		{oceanbase.MetadataColumns, columnQuery, "catalog schema table_name name ordinal_position data_type length precision scale nullable default_value remarks type_owner auto_increment generated", map[int]bool{4: true, 6: true, 7: true, 8: true, 9: true, 13: true, 14: true}, [][]any{
			{catalog, metadataContractSchema, metadataContractName, "c", int64(1), valueformat.TypeNameVarchar, int64(12), nil, nil, int64(1), nil, metadataContractRemark, nil, int64(0), int64(1)},
			{catalog, metadataContractOther, metadataContractName, "c", int64(1), valueformat.TypeNameVarchar, int64(12), nil, nil, int64(1), nil, emptyRemark, nil, int64(1), int64(0)},
			{catalog, "Third", "unannotated", "c", int64(1), valueformat.TypeNameVarchar, int64(12), nil, nil, int64(1), nil, nil, nil, nil, nil},
		}},
	}
}

const (
	metadataContractRemark = " 备注 'quoted' /_% "
	metadataContractSchema = "MiX_S"
	metadataContractOther  = "Other"
	metadataContractName   = "same"
	metadataContractTable  = "TABLE"
	metadataContractView   = "VIEW"
)

const (
	executionColumnQuery   = "SELECT t.base AS visible FROM source_table t"
	executionColumnCatalog = "def"
	executionColumnOrigin  = "base"
)

func refreshedMetadataRemarks(fixtures []metadataContractFixture) []metadataContractFixture {
	var refreshed []metadataContractFixture
	for _, fixture := range fixtures {
		if fixture.kind != oceanbase.MetadataTables && fixture.kind != oceanbase.MetadataColumns {
			continue
		}
		fixture.rows = [][]any{append([]any(nil), fixture.rows[0]...)}
		index := 4
		if fixture.kind == oceanbase.MetadataColumns {
			index = 11
		}
		fixture.rows[0][index] = "updated 备注"
		refreshed = append(refreshed, fixture)
	}
	return refreshed
}

func serveMetadataContract(ctx context.Context, t *testing.T, p *wire.PacketIO, mode oceanbase.Mode, charset string, fixtures []metadataContractFixture, catalogs []string) {
	t.Helper()
	for _, fixture := range fixtures {
		for _, catalog := range catalogs {
			rows := fixture.rows
			if catalog != "" && (mode == oceanbase.ModeOracle || catalog != executionColumnCatalog) {
				rows = nil
			}
			if !testpeer.ReadQuery(ctx, t, p, metadataContractSQL(fixture, mode, charset, catalog)) || !sendMetadataContract(ctx, t, p, fixture, mode, charset, rows) {
				return
			}
		}
	}
	for _, fixture := range refreshedMetadataRemarks(fixtures) {
		if !testpeer.ReadQuery(ctx, t, p, fixture.query) || !sendMetadataContract(ctx, t, p, fixture, mode, charset, fixture.rows) {
			return
		}
	}
	status := byte(2)
	if mode == oceanbase.ModeOracle {
		status = 6
	}
	if !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
		return
	}
	p.ResetSequence()
	testpeer.ServeQuit(t, p)
}

func sendMetadataContract(ctx context.Context, t *testing.T, p *wire.PacketIO, fixture metadataContractFixture, mode oceanbase.Mode, charset string, rows [][]any) bool {
	t.Helper()
	names := strings.Fields(fixture.columns)
	if !testpeer.SendPackets(ctx, t, p, []byte{byte(len(names))}) {
		return false
	}
	collation, status := uint16(45), byte(2)
	if charset == localFilenameCharset {
		collation = 28
	}
	if mode == oceanbase.ModeOracle {
		status = 6
	}
	for i := range names {
		typ := byte(253)
		if fixture.numeric[i] {
			typ = 8
			if mode == oceanbase.ModeOracle {
				typ = 246
			}
		}
		if !testpeer.SendPackets(ctx, t, p, testpeer.TextColumn("dictionary_field", typ, collation, 0)) {
			return false
		}
	}
	if !testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, status, 0}) {
		return false
	}
	for _, values := range rows {
		var row []byte
		for _, value := range values {
			if value == nil {
				row = append(row, 251)
				continue
			}
			text := metadataContractBytes(fmt.Sprint(value), charset)
			row = append(row, byte(len(text)))
			row = append(row, text...)
		}
		if !testpeer.SendPackets(ctx, t, p, row) {
			return false
		}
	}
	return testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, status, 0})
}

func metadataContractBytes(text, charset string) []byte {
	if charset == localFilenameCharset {
		// These two independently specified GBK vectors cover every non-ASCII fixture value.
		text = strings.NewReplacer("备注", "\xb1\xb8\xd7\xa2", "库", "\xbf\xe2").Replace(text)
	}
	return []byte(text)
}

func metadataContractSQL(fixture metadataContractFixture, mode oceanbase.Mode, charset, catalog string) string {
	query, order := fixture.query, ""
	if i := strings.LastIndex(query, " ORDER BY "); i >= 0 {
		query, order = query[:i], query[i+10:]
	}
	if catalog != "" {
		predicate := "'def' =  _" + charset + " X'" + hex.EncodeToString(metadataContractBytes(catalog, charset)) + "' "
		if mode == oceanbase.ModeOracle {
			predicate = "NULL =  '" + strings.ReplaceAll(catalog, "'", "''") + "' "
		}
		query += " AND " + predicate
	}
	if order != "" {
		query += " ORDER BY " + order
	}
	return string(metadataContractBytes(query, charset))
}

const testUTF8MB4 = "utf8mb4"

func complexColumnFixture(version byte) []byte {
	b := testpeer.TextColumn("v", 160, 45, 0)
	return append(b, 1, 'S', 1, 'T', version)
}

func serveComplexDictionary(ctx context.Context, t *testing.T, p *wire.PacketIO, fields []string) bool {
	attrs := make([]oceanbase.Attribute, len(fields))
	for i, name := range fields {
		attrs[i] = oceanbase.Attribute{Name: name, Type: valueformat.TypeNameNumber}
	}
	return serveComplexDictionaryFields(ctx, t, p, "T", attrs)
}

func serveComplexDictionaryFields(ctx context.Context, t *testing.T, p *wire.PacketIO, typ string, fields []oceanbase.Attribute) bool {
	rows := [][]string{{"", "S", typ, valueformat.TypeNameObject, fmt.Sprint(len(fields)), "1", "1"}}
	if !serveDescriptorRows(ctx, t, p, oceanbase.MetadataTypes, "S", typ, rows) {
		return false
	}
	rows = nil
	for i, f := range fields {
		owner := ""
		if !valuecodec.ComplexBuiltinType(f.Type) {
			owner = "S"
		}
		rows = append(rows, []string{"", "S", typ, f.Name, fmt.Sprint(i + 1), f.Type, owner, "", "", ""})
	}
	return serveDescriptorRows(ctx, t, p, oceanbase.MetadataTypeAttributes, "S", typ, rows)
}

func serveDescriptorRows(ctx context.Context, t *testing.T, p *wire.PacketIO, kind oceanbase.MetadataKind, schema, name string, rows [][]string) bool {
	t.Helper()
	plan, err := catalog.Plan(oceanbase.ModeOracle, "", oceanbase.MetadataFilter{Kind: kind, SchemaPattern: catalog.ExactDescriptorPattern(schema), NamePattern: catalog.ExactDescriptorPattern(name)})
	if err != nil {
		t.Fatal(err)
	}
	op := catalogOperation(t, config.Config{Mode: config.ModeOracle}, nil)
	query, err := command.Interpolate(op, plan.SQL, plan.Args)
	if err != nil {
		t.Fatal(err)
	}
	if !testpeer.ReadQuery(ctx, t, p, string(query)) || !testpeer.SendPackets(ctx, t, p, []byte{byte(len(plan.Columns))}) {
		return false
	}
	for _, column := range plan.Columns {
		if !testpeer.SendPackets(ctx, t, p, testpeer.TextColumn(column.Name, 253, 45, 0)) {
			return false
		}
	}
	if !testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, 6, 0}) {
		return false
	}
	for _, row := range rows {
		var data []byte
		for _, value := range row {
			data = append(data, byte(len(value)))
			data = append(data, value...)
		}
		if !testpeer.SendPackets(ctx, t, p, data) {
			return false
		}
	}
	return testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, 6, 0})
}

const (
	testComplexUnchanged = "OLD"
	testComplexAlias     = "ALIAS"
)
