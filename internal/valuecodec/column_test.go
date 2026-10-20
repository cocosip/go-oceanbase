package valuecodec

import (
	"reflect"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestTextScanTypes(t *testing.T) {
	for _, tc := range []struct {
		typ     byte
		charset uint16
		want    reflect.Type
	}{
		{245, 63, reflect.TypeFor[string]()},
		{253, 63, reflect.TypeFor[[]byte]()},
		{254, 63, reflect.TypeFor[[]byte]()},
		{252, 45, reflect.TypeFor[string]()},
		{3, 63, reflect.TypeFor[int64]()},
	} {
		if got := ColumnScanType(wire.Column{Type: tc.typ, Charset: tc.charset}, config.ModeMySQL, config.SessionOptions{}); got != tc.want {
			t.Errorf("type=%d charset=%d: got %v, want %v", tc.typ, tc.charset, got, tc.want)
		}
	}
}
