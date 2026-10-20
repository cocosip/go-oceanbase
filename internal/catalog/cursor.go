package catalog

import (
	"sort"
	"strings"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// CursorIdentity records exact projected base-column and primary-key identities.
type CursorIdentity struct {
	Table, Schema, Name string
	Keys, Columns       []string
	Generated           map[string]bool
	Locking             bool
}

// ResolveCursorIdentity validates projected columns against the table primary-key dictionary.
func ResolveCursorIdentity(mode config.Mode, cols []wire.Column, sets []Result) (*CursorIdentity, error) {
	if len(cols) == 0 || len(sets) != 1 || len(sets[0].Columns) != 8 || len(sets[0].Rows) == 0 {
		return nil, dberrors.ErrCursor
	}
	id, err := ProjectedCursorIdentity(mode, cols)
	if err != nil {
		return nil, err
	}
	projected := make(map[string]bool)
	for _, name := range id.Columns {
		if projected[name] {
			return nil, dberrors.ErrCursor
		}
		projected[name] = true
	}
	type key struct {
		name    string
		ordinal int64
	}
	var keys []key
	seen := make(map[string]bool)
	for _, row := range sets[0].Rows {
		if len(row) != 8 || Text(row[0]) != id.Schema || Text(row[1]) != id.Name || Text(row[2]) != blobBaseTable {
			return nil, dberrors.ErrCursor
		}
		name := Text(row[4])
		if !valueformat.Identifier(name) || seen[name] {
			return nil, dberrors.ErrCursor
		}
		seen[name] = true
		id.Locking = mode == config.ModeOracle || strings.EqualFold(Text(row[3]), blobLockingEngine)
		if !id.Locking {
			return nil, dberrors.ErrUnsupported
		}
		generated := strings.ToUpper(Text(row[6]))
		id.Generated[name] = generated == "YES" || strings.Contains(generated, "VIRTUAL GENERATED") || strings.Contains(generated, "STORED GENERATED")
		if row[7] != nil {
			n, err := Length(row[7])
			if err != nil || n < 1 || !projected[name] || !TableBlobKeyType(mode, strings.ToUpper(Text(row[5]))) {
				return nil, dberrors.ErrCursor
			}
			keys = append(keys, key{name, n})
		}
	}
	if len(keys) == 0 {
		return nil, dberrors.ErrCursor
	}
	for name := range projected {
		if !seen[name] {
			return nil, dberrors.ErrCursor
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].ordinal < keys[j].ordinal })
	for i, k := range keys {
		if k.ordinal != int64(i+1) {
			return nil, dberrors.ErrCursor
		}
		id.Keys = append(id.Keys, k.name)
	}
	return id, nil
}

// ProjectedCursorIdentity extracts a single projected base-table identity.
func ProjectedCursorIdentity(mode config.Mode, cols []wire.Column) (*CursorIdentity, error) {
	first := cols[0]
	id := &CursorIdentity{Schema: first.Schema, Name: first.OriginalTable, Generated: make(map[string]bool)}
	if !valueformat.Identifier(id.Schema) || !valueformat.Identifier(id.Name) {
		return nil, dberrors.ErrCursor
	}
	id.Table = sqltext.QuoteIdentifier(mode, id.Schema) + "." + sqltext.QuoteIdentifier(mode, id.Name)
	for _, col := range cols {
		if col.Schema != id.Schema || col.OriginalTable != id.Name || !valueformat.Identifier(col.OriginalName) {
			return nil, dberrors.ErrCursor
		}
		id.Columns = append(id.Columns, col.OriginalName)
	}
	return id, nil
}
