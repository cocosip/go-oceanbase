package valuecodec

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
)

const parameterBudgetText = "测测"
const parameterBudgetGBK = "\xb2\xe2\xb2\xe2"
const parameterBudgetExpansion = "\x81\x30\x81\x30\x81\x30\x81\x30"

func TestParameterEncodedBudget(t *testing.T) {
	for _, tc := range []struct {
		name, charset, ncharset, text, encoded string
		national                               bool
	}{
		{charsetGBK, charsetGBK, "", parameterBudgetText, parameterBudgetGBK, false},
		{charsetLatin1, charsetLatin1, "", "€€", "\x80\x80", false},
		{charsetGB18030, charsetGB18030, "", "\u0080\u0080", parameterBudgetExpansion, false},
		{charsetUTF8MB4, charsetUTF8MB4, "", parameterBudgetText, parameterBudgetText, false},
		{charsetUTF16, charsetUTF8MB4, charsetUTF16, parameterBudgetText, "\x6d\x4b\x6d\x4b", true},
		{"national-gbk", charsetUTF8MB4, charsetGBK, parameterBudgetText, parameterBudgetGBK, true},
		{"national-fallback", charsetGBK, "", parameterBudgetText, parameterBudgetGBK, true},
		{"national-expansion", charsetUTF8MB4, charsetGB18030, "\u0080\u0080", parameterBudgetExpansion, true},
	} {
		for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
			if tc.national && mode != config.ModeOracle {
				continue
			}
			for _, limit := range []int64{int64(len(tc.encoded)), int64(len(tc.encoded)) - 1, 0, -1} {
				t.Run(fmt.Sprintf("%s/%d/%d", tc.name, mode, limit), func(t *testing.T) {
					var value any = tc.text
					typ := uint16(253)
					if tc.national {
						value, typ = types.NString(tc.text), 208
					}
					p, err := EncodeParameter(value, mode, config.SessionOptions{Charset: tc.charset, NCharset: tc.ncharset}, limit)
					if limit < int64(len(tc.encoded)) {
						if !errors.Is(err, wire.ErrMessageTooLarge) {
							t.Fatal("encoded payload exceeded its budget", err)
						}
						return
					}
					if err != nil || p.Type != typ || !p.LengthEncoded || !bytes.Equal(p.Data, []byte(tc.encoded)) {
						t.Fatal("exact encoded budget rejected or data changed", err)
					}
				})
			}
		}
	}
}
