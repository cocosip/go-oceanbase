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
		mode     config.CompatibilityMode
		limit    int64
		typ      uint16
		data     []byte
		tooLarge bool
	}{
		{"oracle-bytes", []byte{0, 1, 127, 255}, config.CompatibilityModeOracle, 8, 253, []byte("00017fff"), false},
		{"oracle-budget", []byte{0, 1, 127, 255}, config.CompatibilityModeOracle, 7, 0, nil, true},
		{"oracle-nil", []byte(nil), config.CompatibilityModeOracle, 0, 6, nil, false},
		{"oracle-empty", []byte{}, config.CompatibilityModeOracle, 0, 253, []byte{}, false},
		{"oracle-raw", types.Raw{0, 255}, config.CompatibilityModeOracle, 2, 203, []byte{0, 255}, false},
		{"mysql-bytes", []byte{0, 255}, config.CompatibilityModeMySQL, 2, 252, []byte{0, 255}, false},
		{"mysql-budget", []byte{0, 255}, config.CompatibilityModeMySQL, 1, 0, nil, true},
		{"mysql-nil", []byte(nil), config.CompatibilityModeMySQL, 0, 6, nil, false},
		{"mysql-empty", []byte{}, config.CompatibilityModeMySQL, 0, 252, []byte{}, false},
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
