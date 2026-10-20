package catalog_test

import (
	"context"
	"testing"
	"time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestDescriptorPublicSynonym(t *testing.T) {
	op := catalogOperation(t, config.Config{Mode: config.ModeOracle, Limits: config.Limits{MaxMessageBytes: 1 << 16}}, func(ctx context.Context, p *wire.PacketIO) {
		if !serveDescriptorRows(ctx, t, p, oceanbase.MetadataTypes, "S", testComplexAlias, nil) || !serveDescriptorRows(ctx, t, p, oceanbase.MetadataSynonyms, "S", testComplexAlias, nil) || !serveDescriptorRows(ctx, t, p, oceanbase.MetadataSynonyms, "PUBLIC", testComplexAlias, [][]string{{"", "PUBLIC", testComplexAlias, "S", "T", ""}}) || !serveComplexDictionary(ctx, t, p, []string{"N"}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var resolver catalog.Resolver
	d, err := resolver.Resolve(ctx, op, valuecodec.Key{Mode: config.ModeOracle, Schema: "S", Name: testComplexAlias})
	if err != nil || d.Key.Name != "T" || d.Key.Schema != "S" {
		t.Fatal(d, err)
	}
	op.Packets().ResetSequence()
	if err = op.Packets().WriteMessage(ctx, []byte{1}); err != nil {
		t.Fatal(err)
	}
}
