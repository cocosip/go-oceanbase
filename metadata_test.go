package oceanbase

import (
	"context"
	"database/sql"
	"testing"
)

func TestMetadataFilterSessionBoundary(t *testing.T) {
	var nilConn *sql.Conn
	for _, session := range []Session{nil, nilConn, new(sql.DB)} {
		if _, err := Metadata(context.Background(), session, MetadataFilter{Kind: MetadataTables}); err == nil {
			t.Fatal("unpinned session accepted")
		}
	}
}
