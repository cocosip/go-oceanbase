package materialize_test

import (
	context "context"
	driver "database/sql/driver"
	testing "testing"
	time "time"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	catalog "github.com/cocosip/go-oceanbase/internal/catalog"
	materialize "github.com/cocosip/go-oceanbase/internal/materialize"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	valuecodec "github.com/cocosip/go-oceanbase/internal/valuecodec"
	valueformat "github.com/cocosip/go-oceanbase/internal/valueformat"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestComplexDrainLaterRowset(t *testing.T) {
	for _, operation := range []string{"rows-close", "exec"} {
		t.Run(operation, func(t *testing.T) {
			const query = "CALL complex_fixture()"
			c := publictest.OutputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
				if !publictest.ServePrepare(ctx, t, p, query, 23, 0, nil) || !testpeer.ReadCommand(ctx, t, p, publictest.ExecuteHeader(23)) {
					return
				}
				row := wire.AppendComplexLenBytes([]byte{0, 0, 0}, publictest.LobInlineFixture(publictest.TestLOBABC))
				if !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("id", 8, 63, 0), []byte{254, 0, 0, 6, 0}, []byte{0, 0, 1, 0, 0, 0, 0, 0, 0, 0}, []byte{254, 0, 0, 14, 0}, []byte{1}, complexColumnFixture(1), []byte{254, 0, 0, 6, 0}, row, []byte{254, 0, 0, 6, 0}) || !publictest.ServeComplexDictionaryFields(ctx, t, p, "T", []oceanbase.Attribute{{Name: "B", Type: valueformat.TypeNameBlob}}) {
					return
				}
				if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 23)) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 6) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			}, func(cfg *config.Config) { cfg.Limits.MaxMessageBytes = 1 << 16 })
			defer func() { _ = c.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			s, err := c.PrepareContext(ctx, query)
			if err != nil {
				t.Fatal(err)
			}
			if operation == "exec" {
				_, err = s.(driver.StmtExecContext).ExecContext(ctx, nil)
			} else {
				var rows driver.Rows
				rows, err = s.(driver.StmtQueryContext).QueryContext(ctx, nil)
				if err == nil {
					err = rows.Close()
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			if err = c.Ping(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDescriptorBufferedValueRefresh(t *testing.T) {
	op := testpeer.DictionaryOperation(t, config.Config{}, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeComplexType(ctx, t, p, []string{"A"}, []string{valueformat.TypeNameNumber}) {
			return
		}
		testpeer.ServeComplexType(ctx, t, p, []string{"A", "B"}, []string{valueformat.TypeNameNumber, valueformat.TypeNameNumber})
	})
	resolver := new(catalog.Resolver)
	key := valuecodec.Key{Mode: config.ModeOracle, Schema: "S", Name: "T", Version: 1}
	if _, err := resolver.Resolve(t.Context(), op, key); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	decoder := materialize.New(op, resolver)
	value, err := decoder.ComplexValue(ctx, wire.Column{Type: 160, ComplexSchema: "S", ComplexName: "T", ComplexVersion: 1}, []byte{0, 1, '7', 1, '8'})
	var got oceanbase.Object
	if err != nil {
		t.Fatal(err)
	}
	if err = got.Scan(value); err != nil || len(got.Fields) != 2 || got.Fields[1].Name != "B" || got.Fields[1].Value != oceanbase.Number("8") {
		t.Fatal(got, err)
	}
}
