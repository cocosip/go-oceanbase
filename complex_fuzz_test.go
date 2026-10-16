package oceanbase

import (
	"database/sql/driver"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func FuzzComplex(f *testing.F) {
	f.Add([]byte(`{"format":1,"kind":"object","schema":"S","type":"T","version":0,"null":false,"fields":[{"name":"N","type":"NUMBER","value":"1.20"}]}`))
	f.Add([]byte{2, 8, 3, '1', '.', '0'})
	f.Add([]byte{254, 255, 255, 255, 255, 255, 255, 255, 255})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 8192 {
			t.Skip()
		}
		if value, err := scanComplex(data); err == nil {
			raw, err := value.(driver.Valuer).Value()
			if err != nil {
				t.Fatal("accepted document cannot be represented", err)
			}
			if raw != nil {
				if _, err = scanComplex(raw); err != nil {
					t.Fatal("representation cannot be scanned", err)
				}
			}
		}
		shape := &wire.ComplexShape{Array: true, Fields: []*wire.ComplexShape{nil}}
		_, _, _ = wire.DecodeComplex(data, shape, 512)
		_, _ = wire.DecodeBinaryRow(data, []wire.Column{{Type: 160, ComplexShape: shape}, {Type: 8}})
	})
}
