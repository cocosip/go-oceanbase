package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func sensitiveCursorQuery(query string) (string, error) {
	if err := cursorUpdatableQuery(query, ModeOracle, false); err != nil {
		return "", err
	}
	tokens, err := lexSQL(query, ModeOracle, false, false)
	if err != nil || len(tokens) < 4 {
		return "", errors.Join(ErrCursor, err)
	}
	original := query
	for i, token := range tokens {
		if token.kind == 'w' && strings.EqualFold(query[token.start:token.end], "FROM") {
			// Append the hidden column to preserve ordinal ordering and SELECT hints.
			end := tokens[i-1].end
			query = query[:end] + ", ROWID" + query[end:]
			break
		}
	}
	// Oracle requires a qualified wildcard when ROWID is added to the projection.
	if tokens[1].kind == '*' && strings.EqualFold(original[tokens[2].start:tokens[2].end], "FROM") {
		i := 3
		if i+2 < len(tokens) && tokens[i+1].kind == '.' {
			i += 2
		}
		name := original[tokens[i].start:tokens[i].end]
		i++
		if i < len(tokens) && strings.EqualFold(original[tokens[i].start:tokens[i].end], "AS") {
			i++
		}
		if i < len(tokens) && !cursorClause(original, tokens[i]) && cursorIdentifierToken(original, tokens[i], ModeOracle) {
			name = original[tokens[i].start:tokens[i].end]
		}
		query = query[:tokens[1].start] + name + ".*" + query[tokens[1].end:]
	}
	return query, nil
}

func (c *conn) refreshSensitiveRowsLocked(ctx context.Context, handle *resourceHandle, cur *cursorResource, rows [][]driver.Value) ([][]driver.Value, error) {
	if len(cur.columns) < 2 {
		return nil, ErrCursor
	}
	visible := cur.columns[:len(cur.columns)-1]
	id, err := cursorProjectedIdentity(ModeOracle, visible)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(id.columns))
	for i, name := range id.columns {
		names[i] = quoteBlobIdentifier(ModeOracle, name)
	}
	query := "SELECT " + strings.Join(names, ",") + " FROM " + id.table + " WHERE ROWID = ?"
	used := rowsetMetadataSize(visible)
	for i, row := range rows {
		if len(row) != len(cur.columns) || row[len(row)-1] == nil {
			return nil, ErrCursor
		}
		rowid := RowID(tableBlobText(row[len(row)-1]))
		if _, err = rowid.Value(); err != nil {
			return nil, err
		}
		fresh, err := c.readSensitiveRowLocked(ctx, handle, query, rowid)
		if err != nil {
			return nil, err
		}
		if len(fresh) != len(cur.columns)-1 {
			return nil, ErrCursor
		}
		rows[i] = fresh
		used += cursorSnapshotSize(rows[i : i+1])
		if used > c.cfg.Limits.MaxMessageBytes {
			return nil, wire.ErrMessageTooLarge
		}
	}
	return rows, nil
}

func (c *conn) readSensitiveRowLocked(ctx context.Context, handle *resourceHandle, query string, rowid RowID) (_ []driver.Value, err error) {
	prepared, payload, err := c.prepareCursorCommandLocked(ctx, query, []driver.NamedValue{{Ordinal: 1, Value: rowid}})
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, c.closeCursorCommandLocked(prepared)) }()
	if _, err = c.resourceLocked(handle); err != nil {
		return nil, err
	}
	capture := context.WithValue(ctx, lobOpenContextKey{}, &lobOpenState{})
	r, err := c.queryCommandOwnedLocked(capture, payload, true, prepared, true)
	if err != nil {
		return nil, err
	}
	r.holdRelease = true
	sets, err := r.collectLOBResponse(r.ctx)
	if err != nil {
		r.holdRelease = false
		return nil, r.fail(err, true)
	}
	if _, err = c.resourceLocked(handle); err != nil {
		r.holdRelease = false
		return nil, r.fail(err, false)
	}
	if len(sets) != 1 || len(sets[0].rows)+len(sets[0].rawRows) > 1 {
		r.holdRelease = false
		return nil, r.fail(ErrCursor, false)
	}
	err = r.materializeCursorSetLocked(r.ctx, &sets[0])
	if err == nil {
		err = c.materializeRefCursorsLocked(r.ctx, r.ctx, &sets[0], 0, &r.bufferBytes, make(map[refCursorID]bool))
	}
	r.holdRelease = false
	if err != nil {
		return nil, r.fail(err, true)
	}
	r.releaseWithContext(r.ctx)
	if r.err != nil {
		return nil, r.err
	}
	if len(sets[0].rows) == 0 {
		return nil, errors.Join(ErrCursor, sql.ErrNoRows)
	}
	return sets[0].rows[0], nil
}
