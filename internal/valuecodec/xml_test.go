package valuecodec

import (
	"errors"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestXMLTypeScalarIdentity(t *testing.T) {
	c := wire.Column{Type: 160, Charset: 63, ComplexName: "XMLTYPE"}
	options := config.SessionOptions{Charset: "latin1"}
	for _, binary := range []bool{false, true} {
		for _, want := range []string{"", "<x>患者</x>\n"} {
			got, err := DecodeRowValue(c, binary, []byte(want), config.ModeOracle, options)
			if err != nil || got != want {
				t.Fatal(got, err)
			}
		}
		if got, err := DecodeRowValue(c, binary, nil, config.ModeOracle, options); err != nil || got != nil {
			t.Fatal(got, err)
		}
		if _, err := DecodeRowValue(c, binary, []byte{255}, config.ModeOracle, options); !errors.Is(err, charset.ErrEncoding) {
			t.Fatal(err)
		}
		if _, err := DecodeRowValue(c, binary, []byte("<x/>"), config.ModeMySQL, options); err == nil {
			t.Fatal("MySQL XML accepted")
		}
	}
	if TextTypeName(c, config.ModeOracle) != "XMLTYPE" {
		t.Fatal("XML metadata")
	}
	c.ComplexSchema = "APP"
	if TextTypeName(c, config.ModeOracle) != "OBJECT/ARRAY" {
		t.Fatal("user XMLTYPE mistaken for system XML")
	}
}
