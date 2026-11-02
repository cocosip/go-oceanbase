package valuecodec

import (
	"errors"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
)

func TestURowIDParameterIdentity(t *testing.T) {
	const text = "*AAEPAQAAwAEAAAA="
	options := config.SessionOptions{Charset: charsetUTF8MB4}
	for _, tc := range []struct {
		value   any
		null    bool
		literal string
	}{
		{types.URowID(text), false, "CAST('" + text + "' AS UROWID)"},
		{(*types.URowID)(nil), true, "CAST(NULL AS UROWID)"},
	} {
		p, err := EncodeParameter(tc.value, config.ModeOracle, options, 128)
		if err != nil || p.Type != 209 || p.Null != tc.null || !p.LengthEncoded {
			t.Fatalf("identity: %+v, %v", p, err)
		}
		if !tc.null && string(p.Data) != text {
			t.Fatalf("body: %x", p.Data)
		}
		if tc.null && p.EncodedSize() != 0 {
			t.Fatal("typed NULL has body", p)
		}
		literal, err := ClientLiteral(tc.value, config.ModeOracle, options, 128, 128)
		if err != nil || literal != tc.literal {
			t.Fatalf("literal: %q, %v", literal, err)
		}
		if _, err := EncodeParameter(tc.value, config.ModeMySQL, options, 128); !errors.Is(err, dberrors.ErrUnsupported) {
			t.Fatal("MySQL accepted UROWID", err)
		}
		if _, err := ClientLiteral(tc.value, config.ModeMySQL, options, 128, 128); !errors.Is(err, dberrors.ErrUnsupported) {
			t.Fatal("MySQL client accepted UROWID", err)
		}
		if _, err := ClientLiteral(tc.value, config.ModeOracle, options, 128, int64(len(tc.literal)-1)); !errors.Is(err, wire.ErrMessageTooLarge) {
			t.Fatal("literal budget", err)
		}
	}
	if _, err := EncodeParameter(types.URowID(text), config.ModeOracle, options, int64(len(text)-1)); !errors.Is(err, wire.ErrMessageTooLarge) {
		t.Fatal("parameter budget", err)
	}
}
