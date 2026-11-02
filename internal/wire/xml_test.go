package wire

import (
	"bytes"
	"testing"
)

const testXMLSchema = "SYS"
const testXMLTypeName = "XMLTYPE"

func TestXMLTypeIdentityAndFraming(t *testing.T) {
	for _, schema := range []string{"", testXMLSchema, "APP"} {
		c := Column{Type: 160, ComplexSchema: schema, ComplexName: testXMLTypeName}
		want := schema != "APP"
		if c.IsXMLType() != want {
			t.Fatal("wrong XMLTYPE identity", c)
		}
		row := []byte{0, 0, 4, '<', 'x', '/', '>', 7, 0, 0, 0}
		fields, err := DecodeBinaryRow(row, []Column{c, {Type: 3}})
		if !want {
			if err == nil {
				t.Fatal("user XMLTYPE bypassed complex framing")
			}
			continue
		}
		if err != nil || !bytes.Equal(fields[0], []byte("<x/>")) || !bytes.Equal(fields[1], []byte{7, 0, 0, 0}) {
			t.Fatal(fields, err)
		}
		if _, err := DecodeBinaryRow(row[:len(row)-1], []Column{c, {Type: 3}}); err == nil {
			t.Fatal("truncated XML accepted")
		}
	}
	for _, c := range []Column{{Type: 253, ComplexName: testXMLTypeName}, {Type: 160, ComplexSchema: testXMLSchema, ComplexName: "OTHER"}, {Type: 160, ComplexName: testXMLTypeName, ElementType: 160}} {
		if c.IsXMLType() {
			t.Fatal("invalid XML identity", c)
		}
	}
}
