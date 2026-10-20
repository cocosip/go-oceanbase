package catalog

import (
	"testing"

	"github.com/cocosip/go-oceanbase/metadata"
)

func TestMetadataScalarProjection(t *testing.T) {
	projection := MetadataColumns(metadata.MetadataRoutineParameters)
	if len(projection) != 15 || projection[6].Type != 8 || projection[5].Type != 253 || projection[6].Name != "ordinal_position" {
		t.Fatal(projection)
	}
}
