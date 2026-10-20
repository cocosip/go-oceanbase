package wire

import (
	"testing"
)

func FuzzComplex(f *testing.F) {
	f.Add([]byte(`{"format":1,"kind":"object","schema":"S","type":"T","version":0,"null":false,"fields":[{"name":"N","type":"NUMBER","value":"1.20"}]}`))
	f.Add([]byte{2, 8, 3, '1', '.', '0'})
	f.Add([]byte{254, 255, 255, 255, 255, 255, 255, 255, 255})
	// A nested array exhausts 64 nodes before the object's final NULL.
	f.Add([]byte{8, 62, 252, 255, 255, 255, 255, 255, 255, 255})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 8192 {
			t.Skip()
		}
		shape := &ComplexShape{Array: true, Fields: []*ComplexShape{nil}}
		_, _, _ = DecodeComplex(data, shape, 512)
		_, _ = DecodeBinaryRow(data, []Column{{Type: 160, ComplexShape: shape}, {Type: 8}})
		shape = &ComplexShape{Fields: []*ComplexShape{shape, nil}}
		if value, _, err := DecodeComplex(data, shape, 64); err == nil && complexFuzzNodes(value) > 64 {
			t.Fatal("decoded tree exceeds its shared node budget")
		}
	})
}

func complexFuzzNodes(value ComplexValue) int {
	n := 1
	for _, child := range value.Children {
		n += complexFuzzNodes(child)
	}
	return n
}
