package parameters

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/cocosip/go-oceanbase/types"
)

func TestURowIDNormalization(t *testing.T) {
	id := types.URowID("*AAEPAQAAwAEAAAA=")
	for _, value := range []any{id, &id, sql.Null[types.URowID]{V: id, Valid: true}} {
		got, err := normalize(value)
		if err != nil || got != id {
			t.Fatalf("lost native identity: %T %v %v", got, got, err)
		}
	}
	var null *types.URowID
	for _, value := range []any{null, sql.Null[types.URowID]{}, new(sql.Null[types.URowID])} {
		got, err := normalize(value)
		if err != nil || !reflect.DeepEqual(got, null) {
			t.Fatalf("lost typed NULL: %T %v %v", got, got, err)
		}
	}
}
