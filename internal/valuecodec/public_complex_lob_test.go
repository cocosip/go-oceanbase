package valuecodec_test

import (
	"context"
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

const (
	testLOBCollection = "collection"
	testLOBNested     = "nested"
)

func TestNamedComplexLOBInputs(t *testing.T) {
	for _, typ := range []string{valueformat.TypeNameBlob, valueformat.TypeNameClob} {
		for _, shape := range []string{"object", testLOBNested, testLOBCollection} {
			for _, combined := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/%t", typ, shape, combined), func(t *testing.T) {
					const query = "BEGIN p(?); END;"
					data := []byte{0, 1, 128, 255}
					var content any = data
					if typ == valueformat.TypeNameClob {
						data = []byte("患者😀\\'")
						content = string(data)
					}
					value := any(oceanbase.Object{Schema: "S", Type: "T", Fields: []oceanbase.Attribute{{Name: "A", Value: content}, {Name: "N"}}})
					body := wire.AppendComplexLenBytes([]byte{2}, data)
					name := "T"
					if shape == testLOBNested {
						value = oceanbase.Object{Schema: "S", Type: "ROOT", Fields: []oceanbase.Attribute{{Name: "C", Value: value}}}
						body = append([]byte{0}, body...)
						name = "ROOT"
					}
					if shape == testLOBCollection {
						value = oceanbase.Array{Schema: "S", Type: "TA", Elements: []any{content, nil}}
						body = wire.AppendComplexLenBytes([]byte{2, 2}, data)
						name = "TA"
					}
					params := []wire.Parameter{{Type: 160, TypeMetadata: append(append([]byte{1, 'S', byte(len(name))}, name...), 0), Data: body}}
					db := publictest.OutOracleDB(t, config.PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
						if !combined && !publictest.ServePrepareStatus(ctx, t, p, query, 61, 1, nil, 6) {
							return
						}
						if !serveLOBInputDictionary(ctx, t, p, typ, shape) {
							return
						}
						var payload []byte
						var err error
						if combined {
							payload, err = wire.EncodePrepareExecute(0, []byte(query), params, 4096)
						} else {
							payload, err = wire.EncodeStmtExecute(61, params, 4096)
						}
						if err != nil || !testpeer.ReadCommand(ctx, t, p, payload) {
							return
						}
						if combined {
							header := publictest.CombinedHeader(61, 0, 1, 0)
							header[16] = 0
							if !testpeer.SendPackets(ctx, t, p, header, append(testpeer.TextColumn("p", 160, 63, 0), params[0].TypeMetadata...), []byte{254, 0, 0, 6, 0}) {
								return
							}
							p.RestartResponseSequence()
						}
						if !testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, 6, 0, 0, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 61)) {
							return
						}
						p.ResetSequence()
						testpeer.ServeQuit(t, p)
					}, func(cfg *config.Config) { cfg.Execution.PrepareExecute = combined })
					defer func() { _ = db.Close() }()
					if _, err := db.Exec(query, value); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

func serveLOBInputDictionary(ctx context.Context, t *testing.T, p *wire.PacketIO, typ, shape string) bool {
	if shape == testLOBCollection {
		return publictest.ServeDescriptorRows(ctx, t, p, metadata.MetadataTypes, "S", "TA", [][]string{{"", "S", "TA", "COLLECTION", "0", "1", "1"}}) && publictest.ServeDescriptorRows(ctx, t, p, metadata.MetadataCollectionElements, "S", "TA", [][]string{{"", "S", "TA", "TABLE", "", typ, "", "", "", ""}})
	}
	if shape == testLOBNested && !publictest.ServeComplexDictionaryFields(ctx, t, p, "ROOT", []oceanbase.Attribute{{Name: "C", Type: "T"}}) {
		return false
	}
	return publictest.ServeComplexDictionaryFields(ctx, t, p, "T", []oceanbase.Attribute{{Name: "A", Type: typ}, {Name: "N", Type: typ}})
}
