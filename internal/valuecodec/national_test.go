package valuecodec

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/types"
)

const nationalChineseLiteral = "UNISTR('\\4E2D')"
const nationalEmojiLiteral = "UNISTR('\\D83D\\DE00')"
const nationalMixedLiteral = "UNISTR('\\4E2D\\D83D\\DE00')"
const nationalMixedUTF16 = "\x4e\x2d\xd8\x3d\xde\x00"

func TestNStringPrepareCharsets(t *testing.T) {
	for _, tc := range []struct {
		name, sqlCharset, national, input, encoded, literal string
	}{
		{"gbk-valid", charsetUTF8MB4, charsetGBK, "中", "\xd6\xd0", nationalChineseLiteral},
		{"gbk-invalid", charsetUTF8MB4, charsetGBK, "😀", "", ""},
		{"fallback-valid", charsetGBK, "", "中", "\xd6\xd0", nationalChineseLiteral},
		{"fallback-invalid", charsetGBK, "", "😀", "", ""},
		{"ascii-valid", charsetUTF8MB4, charsetASCII, "A", "A", "UNISTR('\\0041')"},
		{"ascii-invalid", charsetUTF8MB4, charsetASCII, "中", "", ""},
		{"utf8mb3-valid", charsetUTF8MB4, charsetUTF8MB3, "中", "中", nationalChineseLiteral},
		{"utf8mb3-invalid", charsetUTF8MB4, charsetUTF8MB3, "😀", "", ""},
		{"latin1-valid", charsetUTF8MB4, charsetLatin1, "€", "\x80", "UNISTR('\\20AC')"},
		{"latin1-invalid", charsetUTF8MB4, charsetLatin1, "中", "", ""},
		{"latin1-sql-utf16-national", charsetLatin1, charsetUTF16, "中😀", nationalMixedUTF16, nationalMixedLiteral},
		{"ascii-sql-utf16-national", charsetASCII, charsetUTF16, "中😀", nationalMixedUTF16, nationalMixedLiteral},
		{"gbk-sql-utf8mb4-national", charsetGBK, charsetUTF8MB4, "😀", "😀", nationalEmojiLiteral},
		{"default-utf8mb4", "", "", "😀", "😀", nationalEmojiLiteral},
		{"empty-national-text", charsetUTF8MB4, charsetUTF16, "", "", "UNISTR('')"},
		{"malformed-national-text", charsetUTF8MB4, charsetUTF16, "\xff", "", ""},
	} {
		for _, prepare := range []config.PrepareMode{config.PrepareServer, config.PrepareClient} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, prepare), func(t *testing.T) {
				options := config.SessionOptions{Charset: tc.sqlCharset, NCharset: tc.national}
				value := types.NString(tc.input)
				if prepare == config.PrepareServer {
					p, err := EncodeParameter(value, config.ModeOracle, options, 1024)
					if tc.literal == "" {
						if !errors.Is(err, charset.ErrEncoding) {
							t.Fatal("unrepresentable national parameter accepted", p, err)
						}
					} else if err != nil || p.Type != 208 || !p.LengthEncoded || !bytes.Equal(p.Data, []byte(tc.encoded)) {
						t.Fatal("national parameter changed", p, err)
					}
					return
				}
				literal, err := ClientLiteral(value, config.ModeOracle, options, 1024, 1024)
				if tc.literal == "" {
					wantErr := charset.ErrEncoding
					if tc.name == "malformed-national-text" {
						wantErr = dberrors.ErrType
					}
					if !errors.Is(err, wantErr) || literal != "" {
						t.Fatal("unrepresentable national literal accepted", literal, err)
					}
				} else if err != nil || literal != tc.literal {
					t.Fatal("national literal changed", literal, err)
				}
			})
		}
	}
}
