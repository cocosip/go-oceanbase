package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

// ColumnChange assigns a value to one projected base column, using its dictionary name.
type ColumnChange struct {
	Name  string
	Value any
}

type cursorOperation uint8

const (
	cursorUpdate cursorOperation = iota
	cursorDelete
	cursorInsert
	cursorRefresh
)

type cursorMutationRequest struct {
	cursor    *Cursor
	operation cursorOperation
	key       map[string]any
	changes   []ColumnChange
}

type cursorIdentity struct {
	table        string
	schema, name string
	keys         []string
	columns      []string
	generated    map[string]bool
	locking      bool
}

// Update changes one row by its complete primary key in the caller's writable transaction.
// Static batches remain the original snapshot; Refresh reads current base-table values.
func (c *Cursor) Update(ctx context.Context, key map[string]any, changes ...ColumnChange) error {
	return c.mutate(ctx, cursorUpdate, key, changes)
}

// Delete removes one row by its complete primary key in the caller's writable transaction.
func (c *Cursor) Delete(ctx context.Context, key map[string]any) error {
	return c.mutate(ctx, cursorDelete, key, nil)
}

// Insert inserts one row with explicit projected columns, including the complete primary key.
func (c *Cursor) Insert(ctx context.Context, changes ...ColumnChange) error {
	return c.mutate(ctx, cursorInsert, nil, changes)
}

func (c *Cursor) mutate(ctx context.Context, op cursorOperation, key map[string]any, changes []ColumnChange) error {
	if err := c.begin(ctx); err != nil {
		return err
	}
	defer c.busy.Store(false)
	request, err := cursorRequest(c, op, key, changes)
	if err != nil {
		return err
	}
	_, err = c.handle.session.ExecContext(ctx, "", request)
	return err
}

// Refresh returns the current projected base-table row for a complete primary key.
// It does not reapply the original SELECT filter or replace a static snapshot.
func (c *Cursor) Refresh(ctx context.Context, key map[string]any) (*sql.Rows, error) {
	if err := c.begin(ctx); err != nil {
		return nil, err
	}
	request, err := cursorRequest(c, cursorRefresh, key, nil)
	if err != nil {
		c.busy.Store(false)
		return nil, err
	}
	rows, err := c.handle.session.QueryContext(ctx, "", request)
	if err != nil {
		c.busy.Store(false)
	}
	return rows, err
}

func cursorRequest(c *Cursor, op cursorOperation, key map[string]any, changes []ColumnChange) (cursorMutationRequest, error) {
	request := cursorMutationRequest{cursor: c, operation: op}
	if op != cursorInsert {
		normalized, err := normalizeBlobTarget(BlobTarget{Table: "t", Column: "c", Key: key})
		if err != nil {
			return request, errors.Join(ErrCursor, err)
		}
		request.key = normalized.Key
	}
	if (op == cursorUpdate || op == cursorInsert) && (len(changes) == 0 || len(changes) > maxTextColumns) {
		return request, ErrCursor
	}
	for _, change := range changes {
		if !validBlobIdentifier(change.Name) {
			return request, ErrCursor
		}
		v, err := convertArgument(change.Value)
		if err != nil {
			return request, err
		}
		if _, out := v.(*outBinding); out {
			return request, ErrCursor
		}
		request.changes = append(request.changes, ColumnChange{change.Name, v})
	}
	return request, nil
}

// Only a simple projection over one base table is eligible; metadata supplies exact identities.
func cursorUpdatableQuery(query string, mode Mode, noBackslash bool) error {
	tokens, err := lexSQL(query, mode, noBackslash, false)
	if err != nil || len(tokens) < 4 {
		return errors.Join(ErrCursor, err)
	}
	from := -1
	for i, t := range tokens {
		word := strings.ToUpper(query[t.start:t.end])
		if t.kind == 'w' {
			switch word {
			case "DISTINCT", "GROUP", "HAVING", "UNION", "INTERSECT", "MINUS", "EXCEPT", "JOIN", "SELECT":
				if i != 0 {
					return ErrCursor
				}
			case "FROM":
				if from != -1 {
					return ErrCursor
				}
				from = i
			}
		}
	}
	if from < 2 {
		return ErrCursor
	}
	// Projection: identifiers, qualification, wildcard, comma, and explicit AS aliases only.
	for _, t := range tokens[1:from] {
		if t.kind != 'w' && t.kind != 'q' && t.kind != '.' && t.kind != '*' && t.kind != ',' {
			return ErrCursor
		}
	}
	i := from + 1
	if i >= len(tokens) || !cursorIdentifierToken(query, tokens[i], mode) {
		return ErrCursor
	}
	i++
	if i < len(tokens) && tokens[i].kind == '.' {
		i++
		if i >= len(tokens) || !cursorIdentifierToken(query, tokens[i], mode) {
			return ErrCursor
		}
		i++
	}
	if i < len(tokens) && strings.EqualFold(query[tokens[i].start:tokens[i].end], "AS") {
		i++
	}
	if i < len(tokens) && !cursorClause(query, tokens[i]) && cursorIdentifierToken(query, tokens[i], mode) {
		i++
	}
	if i < len(tokens) && !cursorClause(query, tokens[i]) {
		return ErrCursor
	}
	return nil
}
func cursorIdentifierToken(query string, t sqlToken, mode Mode) bool {
	if t.kind == 'w' {
		return true
	}
	quote := byte('`')
	if mode == ModeOracle {
		quote = '"'
	}
	return t.kind == 'q' && query[t.start] == quote
}
func cursorClause(query string, t sqlToken) bool {
	if t.kind == ';' {
		return true
	}
	if t.kind != 'w' {
		return false
	}
	switch strings.ToUpper(query[t.start:t.end]) {
	case "WHERE", "ORDER", "LIMIT", "OFFSET", "FETCH", "FOR":
		return true
	}
	return false
}

func resolveCursorIdentity(mode Mode, cols []wire.Column, sets []lobRowset) (*cursorIdentity, error) {
	if len(cols) == 0 || len(sets) != 1 || len(sets[0].columns) != 8 || len(sets[0].rows) == 0 {
		return nil, ErrCursor
	}
	id, err := cursorProjectedIdentity(mode, cols)
	if err != nil {
		return nil, err
	}
	projected := make(map[string]bool)
	for _, name := range id.columns {
		if projected[name] {
			return nil, ErrCursor
		}
		projected[name] = true
	}
	type key struct {
		name    string
		ordinal int64
	}
	var keys []key
	seen := make(map[string]bool)
	for _, row := range sets[0].rows {
		if len(row) != 8 || tableBlobText(row[0]) != id.schema || tableBlobText(row[1]) != id.name || tableBlobText(row[2]) != blobBaseTable {
			return nil, ErrCursor
		}
		name := tableBlobText(row[4])
		if !validBlobIdentifier(name) || seen[name] {
			return nil, ErrCursor
		}
		seen[name] = true
		id.locking = mode == ModeOracle || strings.EqualFold(tableBlobText(row[3]), blobLockingEngine)
		if !id.locking {
			return nil, ErrUnsupported
		}
		generated := strings.ToUpper(tableBlobText(row[6]))
		id.generated[name] = generated == "YES" || strings.Contains(generated, "VIRTUAL GENERATED") || strings.Contains(generated, "STORED GENERATED")
		if row[7] != nil {
			n, err := tableBlobLength(row[7])
			if err != nil || n < 1 || !projected[name] || !tableBlobKeyType(mode, strings.ToUpper(tableBlobText(row[5]))) {
				return nil, ErrCursor
			}
			keys = append(keys, key{name, n})
		}
	}
	if len(keys) == 0 {
		return nil, ErrCursor
	}
	for name := range projected {
		if !seen[name] {
			return nil, ErrCursor
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].ordinal < keys[j].ordinal })
	for i, k := range keys {
		if k.ordinal != int64(i+1) {
			return nil, ErrCursor
		}
		id.keys = append(id.keys, k.name)
	}
	return id, nil
}

func (c *conn) cursorIdentityLocked(ctx context.Context, cur *cursorResource) error {
	columns := cur.columns
	if cur.options.Mode == CursorSensitive && len(columns) > 0 {
		columns = columns[:len(columns)-1]
	}
	if len(columns) == 0 {
		return ErrCursor
	}
	col := columns[0]
	query := mysqlBlobDictionarySQL
	if c.session.mode == ModeOracle {
		query = oracleBlobDictionarySQL
	}
	sets, err := c.tableBlobQueryLocked(ctx, query, namedValues([]driver.Value{col.Schema, col.OriginalTable}), false)
	if err != nil {
		return err
	}
	cur.identity, err = resolveCursorIdentity(c.session.mode, columns, sets)
	return err
}

func (id *cursorIdentity) predicate(mode Mode, key map[string]any) (string, []driver.NamedValue, error) {
	if len(key) != len(id.keys) {
		return "", nil, ErrCursor
	}
	used := make(map[string]bool)
	var predicates []string
	var args []driver.NamedValue
	for i, name := range id.keys {
		value, err := resolveBlobKey(mode, name, key, used)
		if err != nil {
			return "", nil, errors.Join(ErrCursor, err)
		}
		args = append(args, driver.NamedValue{Ordinal: i + 1, Value: value})
		predicates = append(predicates, quoteBlobIdentifier(mode, name)+" = ?")
	}
	return strings.Join(predicates, " AND "), args, nil
}

func (id *cursorIdentity) assignments(mode Mode, changes []ColumnChange) ([]string, []driver.NamedValue, error) {
	used := make(map[string]bool)
	var names []string
	var args []driver.NamedValue
	for _, change := range changes {
		found := ""
		for _, name := range id.columns {
			if matchBlobIdentifier(mode, change.Name, name) {
				if found != "" {
					return nil, nil, ErrCursor
				}
				found = name
			}
		}
		if found == "" || used[found] || id.generated[found] {
			return nil, nil, ErrCursor
		}
		used[found] = true
		names = append(names, quoteBlobIdentifier(mode, found))
		args = append(args, driver.NamedValue{Ordinal: len(args) + 1, Value: change.Value})
	}
	return names, args, nil
}

func (c *conn) cursorMutationLocked(request cursorMutationRequest) (string, []driver.NamedValue, error) {
	record, err := c.resourceLocked(request.cursor.handle)
	if err != nil || record.cursor == nil {
		return "", nil, ErrResource
	}
	id := record.cursor.identity
	if id == nil {
		return "", nil, ErrUnsupported
	}
	if _, ok := request.cursor.handle.session.(*sql.Tx); !ok || c.activeTx == nil || c.activeTx.readOnly || c.session.ok.Status&8192 != 0 || !id.locking {
		return "", nil, ErrUnsupported
	}
	names, values, err := id.assignments(c.session.mode, request.changes)
	if err != nil {
		return "", nil, err
	}
	if request.operation == cursorInsert {
		key := make(map[string]any)
		for i, name := range names {
			for _, primary := range id.keys {
				if name == quoteBlobIdentifier(c.session.mode, primary) {
					key[primary] = values[i].Value
				}
			}
		}
		if _, e := normalizeBlobTarget(BlobTarget{Table: "t", Column: "c", Key: key}); e != nil {
			return "", nil, errors.Join(ErrCursor, e)
		}
		if _, _, err = id.predicate(c.session.mode, key); err != nil {
			return "", nil, err
		}
		return "INSERT INTO " + id.table + " (" + strings.Join(names, ",") + ") VALUES (" + strings.TrimSuffix(strings.Repeat("?,", len(names)), ",") + ")", values, nil
	}
	predicate, args, err := id.predicate(c.session.mode, request.key)
	if err != nil {
		return "", nil, err
	}
	from := " FROM " + id.table + " WHERE " + predicate
	switch request.operation {
	case cursorRefresh:
		var projection []string
		for i, name := range id.columns {
			projection = append(projection, quoteBlobIdentifier(c.session.mode, name)+" AS "+quoteBlobIdentifier(c.session.mode, record.cursor.columns[i].Name))
		}
		return "SELECT " + strings.Join(projection, ",") + from, args, nil
	case cursorDelete:
		return "DELETE" + from, args, nil
	case cursorUpdate:
		for i := range names {
			names[i] += " = ?"
		}
		all := append(values, args...)
		for i := range all {
			all[i].Ordinal = i + 1
		}
		return "UPDATE " + id.table + " SET " + strings.Join(names, ",") + " WHERE " + predicate, all, nil
	}
	return "", nil, ErrCursor
}

func (c *conn) invalidateCursorLocked(cur *Cursor, err error) error {
	if err == nil {
		return nil
	}
	cleanup, cancel := context.WithTimeout(context.Background(), c.cfg.Limits.CleanupTimeout)
	defer cancel()
	if record := c.resources.records[cur.handle.id]; record != nil && record.key == cur.handle.key {
		err = errors.Join(err, c.releaseResourceLocked(cleanup, cur.handle.id, record))
	}
	cur.handle.closed.Store(true)
	return err
}

func (c *conn) execCursorMutation(ctx context.Context, request cursorMutationRequest) (_ driver.Result, err error) {
	if err = c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.unlock()
	c.clearDescriptors()
	query, args, err := c.cursorMutationLocked(request)
	if err != nil {
		return nil, err
	}
	prepared, payload, err := c.prepareCursorCommandLocked(ctx, query, args)
	if err != nil {
		return nil, c.invalidateCursorLocked(request.cursor, err)
	}
	defer func() { err = errors.Join(err, c.closeCursorCommandLocked(prepared)) }()
	defer func() { err = c.invalidateCursorLocked(request.cursor, err) }()
	if request.operation != cursorInsert {
		record, e := c.resourceLocked(request.cursor.handle)
		if e != nil {
			return nil, e
		}
		predicate, key, e := record.cursor.identity.predicate(c.session.mode, request.key)
		if e != nil {
			return nil, e
		}
		sets, e := c.tableBlobQueryLocked(ctx, "SELECT 1 FROM "+record.cursor.identity.table+" WHERE "+predicate+" FOR UPDATE", key, false)
		if e != nil {
			return nil, e
		}
		if len(sets) != 1 || len(sets[0].rows) != 1 {
			return nil, errors.Join(ErrCursor, sql.ErrNoRows)
		}
	}
	if _, err = c.resourceLocked(request.cursor.handle); err != nil {
		return nil, err
	}
	r, err := c.queryCommandOwnedLocked(ctx, payload, true, prepared, true)
	if err != nil {
		return nil, err
	}
	if err = r.drainAll(ctx); err != nil {
		return nil, err
	}
	if _, err = c.resourceLocked(request.cursor.handle); err != nil {
		return nil, err
	}
	if c.session.ok.Warnings != 0 {
		return nil, fmt.Errorf("%w: mutation returned warnings; roll back the caller transaction", ErrCursor)
	}
	if !r.last.known || r.last.affected > 1 || request.operation != cursorUpdate && r.last.affected != 1 {
		return nil, ErrCursor
	}
	return r.last, nil
}

func (c *conn) queryCursorRefresh(ctx context.Context, request cursorMutationRequest) (_ driver.Rows, err error) {
	if request.operation != cursorRefresh {
		return nil, ErrCursor
	}
	if err = c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.unlock()
	c.clearDescriptors()
	query, args, err := c.cursorMutationLocked(request)
	if err != nil {
		return nil, err
	}
	defer func() { err = c.invalidateCursorLocked(request.cursor, err) }()
	prepared, payload, err := c.prepareCursorCommandLocked(ctx, query, args)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, c.closeCursorCommandLocked(prepared)) }()
	if _, err = c.resourceLocked(request.cursor.handle); err != nil {
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
	if _, err = c.resourceLocked(request.cursor.handle); err != nil {
		r.holdRelease = false
		return nil, r.fail(err, false)
	}
	if len(sets) != 1 || len(sets[0].rows)+len(sets[0].rawRows) != 1 {
		r.holdRelease = false
		return nil, r.fail(errors.Join(ErrCursor, sql.ErrNoRows), false)
	}
	err = r.materializeCursorSetLocked(r.ctx, &sets[0])
	r.holdRelease = false
	if err != nil {
		return nil, r.fail(err, true)
	}
	r.releaseWithContext(r.ctx)
	if r.err != nil {
		return nil, r.err
	}
	if _, err = c.resourceLocked(request.cursor.handle); err != nil {
		return nil, err
	}
	buffered := &textRows{conn: c, ctx: ctx, iterationCtx: ctx, columns: sets[0].columns, released: true, buffered: sets}
	return &cursorBatchRows{textRows: buffered, cursor: request.cursor}, nil
}

func (r *textRows) materializeCursorSetLocked(ctx context.Context, set *lobRowset) error {
	if err := r.decodeComplexRowsetLocked(ctx, set); err != nil {
		return err
	}
	for _, row := range set.rows {
		before := lobValuesSize(row)
		if err := r.conn.materializeLOBValuesLocked(ctx, set.columns, row, r.conn.cfg.Limits.MaxMessageBytes-r.bufferBytes+before); err != nil {
			return err
		}
		r.bufferBytes += lobValuesSize(row) - before
		if r.bufferBytes > r.conn.cfg.Limits.MaxMessageBytes {
			return wire.ErrMessageTooLarge
		}
	}
	return nil
}

func cursorProjectedIdentity(mode Mode, cols []wire.Column) (*cursorIdentity, error) {
	first := cols[0]
	id := &cursorIdentity{schema: first.Schema, name: first.OriginalTable, generated: make(map[string]bool)}
	if !validBlobIdentifier(id.schema) || !validBlobIdentifier(id.name) {
		return nil, ErrCursor
	}
	id.table = quoteBlobIdentifier(mode, id.schema) + "." + quoteBlobIdentifier(mode, id.name)
	for _, col := range cols {
		if col.Schema != id.schema || col.OriginalTable != id.name || !validBlobIdentifier(col.OriginalName) {
			return nil, ErrCursor
		}
		id.columns = append(id.columns, col.OriginalName)
	}
	return id, nil
}

func (c *conn) closeCursorCommandLocked(prepared *stmt) error {
	cleanup, cancel := context.WithTimeout(context.Background(), c.cfg.Limits.CleanupTimeout)
	defer cancel()
	return prepared.closeLocked(cleanup)
}

func (c *conn) prepareCursorCommandLocked(ctx context.Context, query string, args []driver.NamedValue) (*stmt, []byte, error) {
	prepared, err := c.prepareServerLocked(ctx, query)
	if err != nil {
		return nil, nil, err
	}
	params, err := c.cursorParametersLocked(ctx, prepared.plan, args)
	var payload []byte
	if err == nil {
		payload, err = wire.EncodeStmtExecute(prepared.id, params, c.cfg.Limits.MaxMessageBytes)
	}
	if err != nil {
		return nil, nil, errors.Join(err, c.closeCursorCommandLocked(prepared))
	}
	return prepared, payload, nil
}
