package valuecodec_test

import (
	"encoding/hex"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
	"strings"
	"testing"
)

func TestJavaParityCharsetSQLParametersAndResults(t *testing.T) {
	for _, tc := range []struct {
		name, text, data string
		collation        uint16
	}{
		{"hkscs", "镬😀", "镬😀", 152}, {"hkscs31", "镬😀", "镬😀", 153}, {"gb18030_2022", "龴\ufe10\ue000", "\xfe\x59\xa6\xd9\xaa\xa1", 217},
		{"sjis", "日本\\'", "\x93\xfa\x96\x7b\\'", 13}, {"big5", "中文\\'", "\xa4\xa4\xa4\xe5\\'", 1},
		{"gb2312", "中文", "\xd6\xd0\xce\xc4", 24}, {"euckr", "한글", "\xc7\xd1\xb1\xdb", 19},
		{"dec8", "ŒŸ", "\xd7\xdd", 3}, {"tis620", "กข", "\xa1\xa2", 18},
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
			if mode == config.ModeOracle && string(sql) != "'"+strings.ReplaceAll(tc.data, "'", "''")+"'" {
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

func TestAddedCharsetColumnLengths(t *testing.T) {
	for _, tc := range []struct {
		collation uint16
		width     uint32
	}{
		{13, 2}, {84, 2}, {24, 2}, {85, 2}, {3, 1}, {18, 1}, {56, 4}, {363, 4},
	} {
		column := wire.Column{Type: 253, Charset: tc.collation, Length: 12 * tc.width}
		if length, ok := valuecodec.ColumnLength(column, config.ModeMySQL); !ok || length != 12 {
			t.Errorf("collation %d length: %d, %v", tc.collation, length, ok)
		}
	}
	for _, binaryRows := range []bool{false, true} {
		v, err := valuecodec.DecodeRowValue(wire.Column{Type: 253, Charset: 56}, binaryRows, []byte{0x2d, 0x4e, 0x3d, 0xd8, 0x00, 0xde}, config.ModeMySQL, config.SessionOptions{Charset: charsetUTF8MB4})
		if err != nil || v != "中😀" {
			t.Fatalf("UTF16LE result: %v, %v", v, err)
		}
	}
}
