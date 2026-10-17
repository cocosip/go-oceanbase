package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync/atomic"
)

// ErrResource identifies a closed resource or a handle whose owning session has ended.
var ErrResource = errors.New("oceanbase: resource is no longer valid")

// Nonzero size gives every live identity a distinct address, without a global registry.
type resourceEpoch struct{ _ byte }

type resourceKey struct {
	connection, session, transaction *resourceEpoch
}

type resourceHandle struct {
	session Session
	key     resourceKey
	id      *resourceEpoch
	closed  atomic.Bool
}

type resourceRecord struct {
	key       resourceKey
	parent    *stmt
	published bool
	lob       *nativeLOBResource
	tableBlob *tableBlobResource
	cursor    *cursorResource
}

type resourceState struct {
	key     resourceKey
	records map[*resourceEpoch]*resourceRecord
}

type resourceRequest struct{ handle *resourceHandle }

func (h *resourceHandle) close(ctx context.Context) error {
	if h == nil {
		return nil
	}
	if !validSession(h.session) {
		return ErrResource
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if h.closed.Swap(true) {
		return nil
	}
	_, err := h.session.ExecContext(ctx, "", resourceRequest{handle: h})
	if errors.Is(err, ErrResource) || errors.Is(err, sql.ErrConnDone) || errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	if err != nil {
		// database/sql can cancel before dispatch; a second close must still release the record.
		h.closed.Store(false)
	}
	return err
}

func (c *conn) registerResourceLocked(session Session, parent *stmt) (*resourceHandle, error) {
	if !validSession(session) || c.closed.Load() {
		return nil, ErrResource
	}
	_, transaction := session.(*sql.Tx)
	if transaction != (c.activeTx != nil) {
		return nil, ErrResource
	}
	if parent != nil && (parent.conn != c || parent.closed || parent.closeRequested) {
		return nil, ErrResource
	}
	if c.resources.records == nil {
		c.resources.key = resourceKey{new(resourceEpoch), new(resourceEpoch), new(resourceEpoch)}
		c.resources.records = make(map[*resourceEpoch]*resourceRecord)
	}
	h := &resourceHandle{session: session, key: c.resources.key, id: new(resourceEpoch)}
	c.resources.records[h.id] = &resourceRecord{key: h.key, parent: parent}
	if parent != nil {
		parent.leases++
	}
	return h, nil
}

func (c *conn) publishResourceLocked(h *resourceHandle) error {
	record, err := c.resourceLocked(h)
	if err != nil {
		return err
	}
	record.published = true
	return nil
}

func (c *conn) resourceLocked(h *resourceHandle) (*resourceRecord, error) {
	if h == nil || h.closed.Load() {
		return nil, ErrResource
	}
	return c.resourceRecordLocked(h)
}

func (c *conn) resourceRecordLocked(h *resourceHandle) (*resourceRecord, error) {
	if h == nil || c.closed.Load() || h.key != c.resources.key {
		return nil, ErrResource
	}
	record := c.resources.records[h.id]
	if record == nil || record.key != h.key {
		return nil, ErrResource
	}
	return record, nil
}

func (c *conn) execResource(ctx context.Context, request resourceRequest) (driver.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Limits.CleanupTimeout)
	defer cancel()
	if err := c.lock(ctx); err != nil {
		c.discard()
		return nil, err
	}
	defer c.unlock()
	record, err := c.resourceRecordLocked(request.handle)
	if err != nil {
		return nil, err
	}
	if err = c.releaseResourceLocked(ctx, request.handle.id, record); err != nil {
		return nil, err
	}
	return result{known: true}, nil
}

func (c *conn) releaseResourceLocked(ctx context.Context, id *resourceEpoch, record *resourceRecord) error {
	delete(c.resources.records, id)
	var err error
	if record.lob != nil {
		err = c.freeLOBLocked(ctx, record.lob)
	}
	if s := record.parent; s != nil {
		s.leases--
		if s.leases == 0 && s.closeRequested {
			return errors.Join(err, s.closeLocked(ctx))
		}
	}
	return err
}

// Only complete response boundaries may release server resources.
func (c *conn) cleanupResourcesLocked(ctx context.Context, all bool) error {
	var err error
	for id, record := range c.resources.records {
		if all || !record.published || record.key != c.resources.key {
			err = errors.Join(err, c.releaseResourceLocked(ctx, id, record))
		}
	}
	return err
}

func (c *conn) invalidateResourcesLocked(ctx context.Context, reset bool) error {
	if c.resources.records == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Limits.CleanupTimeout)
	defer cancel()
	if reset {
		c.resources.key.session = new(resourceEpoch)
	}
	c.resources.key.transaction = new(resourceEpoch)
	return c.cleanupResourcesLocked(ctx, true)
}

func (c *conn) observeResourceStatus(status uint16) {
	previous := c.session.ok.Status
	// A managed transaction can start lazily; its first DML stays in the same epoch.
	ended := previous&1 != 0 && status&1 == 0
	started := previous&1 == 0 && status&1 != 0 && c.activeTx == nil
	if c.resources.records != nil && (ended || started || previous&2 != status&2) {
		c.resources.key.transaction = new(resourceEpoch)
	}
}
