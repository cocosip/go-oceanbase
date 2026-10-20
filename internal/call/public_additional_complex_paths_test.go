package call_test

import (
	context "context"
	sql "database/sql"
	driver "database/sql/driver"
	errors "errors"
	testing "testing"
	time "time"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestComplexOutputs(t *testing.T) {
	for _, tailError := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "tail-error"}[tailError], func(t *testing.T) {
			db := publictest.OutOracleDB(t, config.PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
				want, err := wire.EncodePrepareExecute(0, []byte("BEGIN p(?); END;"), []wire.Parameter{{Type: 6}}, 4096)
				if err != nil || !testpeer.ReadCommand(ctx, t, p, want) {
					return
				}
				if !testpeer.SendPackets(ctx, t, p, publictest.CombinedHeader(21, 1, 1, 1), testpeer.TextColumn("p", 6, 63, 0), []byte{254, 0, 0, 6, 0}, complexColumnFixture(7), []byte{254, 0, 0, 6, 16}, []byte{0, 0, 0, 1, '7'}, []byte{254, 0, 0, 6, 0}) {
					return
				}
				tail := []byte{0, 0, 0, 6, 0, 0, 0}
				if tailError {
					tail = []byte{255, 1, 0, '#', 'H', 'Y', '0', '0', '0', 'x'}
				}
				if !testpeer.SendPackets(ctx, t, p, tail) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 21)) {
					return
				}
				if !tailError && !publictest.ServeComplexDictionary(ctx, t, p, []string{"N"}) {
					return
				}
				if !testpeer.ServeOK(ctx, t, p, []byte{14}, 6) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			v := oceanbase.Object{Type: testComplexUnchanged}
			_, err := db.ExecContext(ctx, "BEGIN p(?); END;", sql.Out{Dest: &v})
			if tailError {
				var server *oceanbase.Error
				if !errors.As(err, &server) || v.Type != testComplexUnchanged {
					t.Fatal(v, err)
				}
			} else if err != nil || v.Type != "T" || v.Fields[0].Value != oceanbase.Number("7") {
				t.Fatal(v, err)
			}
			if err = db.PingContext(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestComplexNullConversion(t *testing.T) {
	var o *oceanbase.Object
	for _, value := range []any{o, oceanbase.Object{Null: true}, oceanbase.Array{Null: true}} {
		var named = driver.NamedValue{Value: value}
		if _, err := normalizeArgument(t, named.Value); err != nil {
			t.Fatal(err)
		}
	}
}
