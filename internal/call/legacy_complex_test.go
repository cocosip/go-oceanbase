package call

import (
	"context"
	"errors"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestComplexOutputColumnMismatch(t *testing.T) {
	op := testpeer.DictionaryOperation(t, config.Config{}, func(ctx context.Context, p *wire.PacketIO) {
		testpeer.ServeComplexType(ctx, t, p, []string{"N"}, []string{valueformat.TypeNameNumber})
	})
	resolver := new(catalog.Resolver)
	_, err := resolver.Resolve(t.Context(), op, valuecodec.Key{Mode: config.ModeOracle, Schema: "S", Name: "T"})
	if err != nil {
		t.Fatal(err)
	}
	column := wire.Column{Type: 160, ComplexSchema: "S", ComplexName: "T"}
	s := &State{bindings: []*Binding{{}}, physical: []int{0}, complexColumns: []wire.Column{column, column}, complexRows: [][]byte{{0, 0, 0, 1, '7', 0, 1, '8'}}}
	if err := s.DecodeComplex(context.Background(), op, resolver); !errors.Is(err, dberrors.ErrOut) {
		t.Fatal(err)
	}
	if s.values != nil || s.columns != nil {
		t.Fatal("invalid OUT response published values or columns")
	}
}
