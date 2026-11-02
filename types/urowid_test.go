package types

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/cocosip/go-oceanbase/dberrors"
)

func TestURowID(t *testing.T) {
	const text = "*AAEPAQAAwAEAAAA="
	var id URowID
	if err := id.Scan([]byte(text)); err != nil || id != text {
		t.Fatalf("scan: %q, %v", id, err)
	}
	if value, err := id.Value(); err != nil || value != text {
		t.Fatalf("value: %v, %v", value, err)
	}
	for _, invalid := range []any{nil, "", "*", "a' OR 1=1", "a\x00", "**AA", int64(1)} {
		old := id
		if err := id.Scan(invalid); !errors.Is(err, dberrors.ErrType) || id != old {
			t.Fatalf("invalid changed receiver: %v %q %v", invalid, id, err)
		}
	}
	if err := (*URowID)(nil).Scan(text); !errors.Is(err, dberrors.ErrType) {
		t.Fatal(err)
	}
	var nullable sql.Null[URowID]
	if err := nullable.Scan(text); err != nil || !nullable.Valid || nullable.V != text {
		t.Fatal(nullable, err)
	}
	if err := nullable.Scan(nil); err != nil || nullable.Valid {
		t.Fatal(nullable, err)
	}
}
