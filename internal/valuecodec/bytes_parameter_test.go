package valuecodec

import (
	"bytes"
	"errors"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
)

func TestBytesParameterEncoding(t *testing.T) {
	for _, tc := range []struct {
		name     string
		value    any
		mode     config.Mode
		limit    int64
		typ      uint16
		data     []byte
		tooLarge bool
	}{
		{"oracle-bytes", []byte{0, 1, 127, 255}, config.ModeOracle, 8, 253, []byte("00017fff"), false},
		{"oracle-budget", []byte{0, 1, 127, 255}, config.ModeOracle, 7, 0, nil, true},
		{"oracle-nil", []byte(nil), config.ModeOracle, 0, 6, nil, false},
		{"oracle-empty", []byte{}, config.ModeOracle, 0, 253, []byte{}, false},
		{"oracle-raw", types.Raw{0, 255}, config.ModeOracle, 2, 203, []byte{0, 255}, false},
		{"mysql-bytes", []byte{0, 255}, config.ModeMySQL, 2, 252, []byte{0, 255}, false},
		{"mysql-budget", []byte{0, 255}, config.ModeMySQL, 1, 0, nil, true},
		{"mysql-nil", []byte(nil), config.ModeMySQL, 0, 6, nil, false},
		{"mysql-empty", []byte{}, config.ModeMySQL, 0, 252, []byte{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := EncodeParameter(tc.value, tc.mode, config.SessionOptions{}, tc.limit)
			if tc.tooLarge {
				if !errors.Is(err, wire.ErrMessageTooLarge) {
					t.Fatal(p, err)
				}
				return
			}
			if err != nil || p.Type != tc.typ || !bytes.Equal(p.Data, tc.data) || p.Type != 6 && !p.LengthEncoded {
				t.Fatalf("parameter=%+v error=%v", p, err)
			}
		})
	}
}
