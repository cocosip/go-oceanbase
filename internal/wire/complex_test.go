package wire

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

func TestComplexNestedNullNodeBudget(t *testing.T) {
	child := &ComplexShape{Fields: []*ComplexShape{nil, nil}}
	for _, tc := range []struct {
		name  string
		shape *ComplexShape
		data  []byte
	}{
		{"object", &ComplexShape{Fields: []*ComplexShape{child, nil}}, []byte{8, 12}},
		{"array", &ComplexShape{Array: true, Fields: []*ComplexShape{child}}, []byte{2, 8, 12}},
	} {
		for _, collect := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/collect=%t", tc.name, collect), func(t *testing.T) {
				// Root, child container, its two NULLs, and a trailing NULL: five nodes.
				for _, limit := range []int{4, 5} {
					r := decoder{data: tc.data}
					nodes := limit
					value, err := r.complex(tc.shape, 0, &nodes, collect)
					if limit == 4 {
						if !errors.Is(err, ErrMessageTooLarge) || nodes < 0 {
							t.Fatalf("accepted a trailing NULL beyond the node budget: nodes=%d err=%v", nodes, err)
						}
						continue
					}
					if err != nil || nodes != 0 || r.offset != len(tc.data) {
						t.Fatal("exact node budget rejected", nodes, r.offset, err)
					}
					if collect && (len(value.Children) != 2 || !value.Children[1].Null || len(value.Children[0].Children) != 2) {
						t.Fatal("nested NULL tree changed", value)
					}
				}
			})
		}
	}
}

func TestComplexBinaryRowNodeBudget(t *testing.T) {
	shape := &ComplexShape{Fields: []*ComplexShape{{Array: true, Fields: []*ComplexShape{nil}}, nil}}
	for _, count := range []int{65533, 65534} {
		// Root + array + count NULL elements + one trailing NULL.
		data := appendLenEnc([]byte{8}, uint64(count))
		bitmap := make([]byte, (count+9)/8)
		for i := range count {
			bitmap[(i+2)/8] |= 1 << uint((i+2)%8)
		}
		data = append(data, bitmap...)
		row := append([]byte{0, 0}, data...)
		row = append(row, 42, 0, 0, 0)
		fields, err := DecodeBinaryRow(row, []Column{{Type: 160, ComplexShape: shape}, {Type: 3}})
		_, consumed, treeErr := DecodeComplex(data, shape, 65536)
		if count == 65534 {
			if !errors.Is(err, ErrMessageTooLarge) || !errors.Is(treeErr, ErrMessageTooLarge) {
				t.Fatal("binary row accepted too many NULL nodes", err, treeErr)
			}
		} else if err != nil || treeErr != nil || consumed != len(data) || len(fields) != 2 || !bytes.Equal(fields[1], []byte{42, 0, 0, 0}) {
			t.Fatal("exact binary node limit changed framing", fields, consumed, err, treeErr)
		}
	}
}

func TestComplexNullDepth(t *testing.T) {
	shape := &ComplexShape{Fields: []*ComplexShape{nil}}
	for _, collect := range []bool{false, true} {
		for _, depth := range []int{31, 32} {
			nodes := 2
			r := decoder{data: []byte{4}}
			_, err := r.complex(shape, depth, &nodes, collect)
			if depth == 32 {
				if !errors.Is(err, ErrMessageTooLarge) {
					t.Fatal("NULL bypassed maximum depth", collect, err)
				}
			} else if err != nil || nodes != 0 {
				t.Fatal("NULL at exact depth limit was rejected", collect, nodes, err)
			}
		}
	}
}

func TestComplexFraming(t *testing.T) {
	shape := &ComplexShape{Fields: []*ComplexShape{nil, {Array: true, Fields: []*ComplexShape{nil}}}}
	column := Column{Type: 160, ComplexShape: shape}
	// Row bitmap, object bitmap, NUMBER, array count/bitmap, RAW, following integer.
	data := []byte{0, 0, 0, 3, '1', '.', '0', 2, 8, 2, 0, 255, 42, 0, 0, 0}
	fields, err := DecodeBinaryRow(data, []Column{column, {Type: 3}})
	if err != nil || len(fields) != 2 || !bytes.Equal(fields[1], []byte{42, 0, 0, 0}) {
		t.Fatal(fields, err)
	}
	v, n, err := DecodeComplex(fields[0], shape, 64)
	if err != nil || n != len(fields[0]) || string(v.Children[0].Data) != "1.0" || !v.Children[1].Children[1].Null {
		t.Fatal(v, n, err)
	}
	if _, _, err = DecodeComplex(fields[0], shape, 2); err == nil {
		t.Fatal("node budget ignored")
	}
}

func TestComplexParameterHeader(t *testing.T) {
	p := Parameter{Type: 160, TypeMetadata: []byte{1, 'S', 1, 'T', 0}, Data: []byte{0, 1, '7'}}
	got, err := EncodeStmtExecute(1, []Parameter{p}, 23)
	want := []byte{23, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 160, 0, 1, 'S', 1, 'T', 0, 0, 1, '7'}
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal(got, err)
	}
	if _, err = EncodeStmtExecute(1, []Parameter{p}, int64(len(want)-1)); err != ErrMessageTooLarge {
		t.Fatal("metadata missing from size budget", err)
	}
}
