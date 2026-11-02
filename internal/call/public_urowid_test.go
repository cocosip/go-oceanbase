package call_test

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
)

const testURowIDExplicit = "explicit"
const testURowIDInout = "inout"
const testURowIDNullInout = "null-inout"
const testURowIDNullOut = "null-out"

func TestURowIDOutputs(t *testing.T) {
	const text = "*AAEPAQAAwAEAAAA="
	for _, prepare := range []config.PrepareMode{config.PrepareServer, config.PrepareClient} {
		for _, name := range []string{"out", testURowIDInout, testURowIDExplicit, testURowIDNullInout, testURowIDNullOut} {
			t.Run(fmt.Sprint(prepare)+"/"+name, func(t *testing.T) {
				value := oceanbase.URowID(text)
				plain := text
				nullable := sql.Null[oceanbase.URowID]{}
				out := oceanbase.Out{Dest: &value, In: name == testURowIDInout}
				param := wire.Parameter{Type: 209, Null: true, LengthEncoded: true}
				if name == testURowIDExplicit {
					out = oceanbase.Out{Dest: &plain, In: true, Type: valueformat.TypeNameURowID, Length: 64}
				}
				if name == testURowIDNullInout || name == testURowIDNullOut {
					out = oceanbase.Out{Dest: &nullable, In: name == testURowIDNullInout}
				}
				if name == testURowIDInout || name == testURowIDExplicit {
					param.Null, param.Data = false, []byte(text)
				}
				row := append([]byte{0, 0, byte(len(text))}, text...)
				if name == testURowIDNullOut {
					row = []byte{0, 4}
				}
				db := publictest.OutOracleDB(t, prepare, func(ctx context.Context, p *wire.PacketIO) {
					if !publictest.ServeLOBCall(ctx, t, p, "CALL p(?)", []wire.Parameter{param}, [][]byte{testpeer.TextColumn("out", 209, 63, 0)}, row) {
						return
					}
					p.ResetSequence()
					testpeer.ServeQuit(t, p)
				})
				defer func() { _ = db.Close() }()
				if _, err := db.ExecContext(context.Background(), "CALL p(?)", out); err != nil {
					t.Fatal(err)
				}
				if name == testURowIDNullOut {
					if nullable.Valid {
						t.Fatal("NULL output lost")
					}
				} else if name == testURowIDNullInout {
					if !nullable.Valid || nullable.V != value {
						t.Fatal(nullable)
					}
				} else if value != text || plain != text {
					t.Fatal(value, plain)
				}
			})
		}
	}
}
