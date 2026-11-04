package catalog

import (
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestAnonymousTemporalNationalDescriptor(t *testing.T) {
	for _, tc := range []struct {
		code byte
		typ  string
	}{{202, "TIMESTAMP"}, {207, "NVARCHAR2"}, {208, "NCHAR"}} {
		d, err := AnonymousDescriptor(wire.Column{ElementType: tc.code, ComplexVersion: 7})
		if err != nil || !d.Array || d.Key.Version != 7 || d.Key.Name != "" || len(d.Fields) != 1 || d.Fields[0].Type != tc.typ {
			t.Fatalf("anonymous %s descriptor: %v %v", tc.typ, d, err)
		}
	}
}
