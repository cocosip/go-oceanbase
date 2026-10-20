package valuecodec_test

import (
	"encoding/hex"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
	"testing"
)

func TestJavaParityCharsetSQLParametersAndResults(t *testing.T) {
	for _, tc := range []struct {
		name, text, data string
		collation        uint16
	}{
		{"hkscs", "镬😀", "镬😀", 152}, {"hkscs31", "镬😀", "镬😀", 153}, {"gb18030_2022", "龴\ufe10\ue000", "\xfe\x59\xa6\xd9\xaa\xa1", 217},
	} {
		for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
			options := config.SessionOptions{Charset: tc.name}
			sql, err := command.EncodeSQL("SELECT '"+tc.text+"'", options.Charset, 4096)
			if err != nil || string(sql) != "SELECT '"+tc.data+"'" {
				t.Fatal(tc.name, sql, err)
			}
			p, err := valuecodec.EncodeParameter(tc.text, mode, options, 4096)
			if err != nil || string(p.Data) != tc.data {
				t.Fatal(tc.name, p, err)
			}
			literal, err := valuecodec.ClientLiteral(tc.text, mode, options, 4096, 4096)
			if err != nil {
				t.Fatal(tc.name, err)
			}
			sql, err = command.EncodeSQL(literal, options.Charset, 4096)
			if err != nil {
				t.Fatal(err)
			}
			if mode == config.ModeOracle && string(sql) != "'"+tc.data+"'" {
				t.Fatal("client bind", tc.name, sql)
			}
			if mode == config.ModeMySQL && literal != "_"+tc.name+" X'"+hex.EncodeToString([]byte(tc.data))+"'" {
				t.Fatal("client bind", tc.name, literal)
			}
			for _, binaryRows := range []bool{false, true} {
				v, err := valuecodec.DecodeRowValue(wire.Column{Type: 253, Charset: tc.collation}, binaryRows, []byte(tc.data), mode, options)
				if err != nil || v != tc.text {
					t.Fatal(tc.name, v, err)
				}
			}
			if mode == config.ModeOracle {
				options.NCharset = tc.name
				national, nationalErr := valuecodec.EncodeParameter(types.NString(tc.text), mode, options, 4096)
				if nationalErr != nil || string(national.Data) != tc.data {
					t.Fatal("national parameter", tc.name, national, nationalErr)
				}
				v, nationalErr := valuecodec.DecodeRowValue(wire.Column{Type: 208, Charset: 63}, true, []byte(tc.data), mode, options)
				if nationalErr != nil || v != tc.text {
					t.Fatal("national result", tc.name, v, nationalErr)
				}
			}
		}
	}
}
