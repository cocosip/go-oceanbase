package routing

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Discovery errors deliberately exclude source URLs, filenames and response text.
var (
	ErrClosed      = errors.New("routing: connector closed")
	ErrUnavailable = errors.New("routing: discovery unavailable")
)

// DiscoveredEndpoint carries the source's explicit logical service role.
type DiscoveredEndpoint struct {
	Address
	Role uint8
}

// Snapshot contains only service identity and candidates, never credentials or session state.
type Snapshot struct {
	Service   string
	Endpoints []DiscoveredEndpoint
}

// Lookup must honor cancellation and return an owned snapshot.
type Lookup func(context.Context) (Snapshot, error)

// DiscoveryOptions are fixed for the Connector lifetime.
type DiscoveryOptions struct {
	Service                                 string
	Role                                    uint8
	RefreshInterval, RequestTimeout, MaxAge time.Duration
	Selection                               Options
}

// Discovery serializes source access and publishes validated immutable candidate generations.
type Discovery struct {
	mu              sync.Mutex
	source          Lookup
	options         DiscoveryOptions
	ctx             context.Context
	cancel          context.CancelFunc
	started, closed bool
	failed          bool
	done, changed   chan struct{}
	routes          *Selector
	count           int
	fetched         time.Time
}

func validService(s string) bool {
	return s != "" && len(s) <= 1024 && strings.IndexFunc(s, unicode.IsControl) < 0 && strings.TrimSpace(s) == s
}

func validateSnapshot(s Snapshot, service string, role uint8) (Snapshot, error) {
	if !validService(s.Service) || s.Service != service || len(s.Endpoints) == 0 || len(s.Endpoints) > 1024 {
		return Snapshot{}, ErrInvalid
	}
	s.Endpoints = slices.Clone(s.Endpoints)
	seen := map[string]bool{}
	eligible := 0
	for i := range s.Endpoints {
		e := &s.Endpoints[i]
		if e.Role > 2 || e.Addr == "" {
			return Snapshot{}, ErrInvalid
		}
		addr, err := NormalizeAddress(e.Addr)
		if err != nil || seen[addr] {
			return Snapshot{}, ErrInvalid
		}
		e.Addr = addr
		seen[addr] = true
		if role == 0 || e.Role == role {
			eligible++
		}
	}
	if eligible == 0 {
		return Snapshot{}, ErrInvalid
	}
	return s, nil
}

// NewDiscovery does no I/O and starts no goroutine until Get is first called.
func NewDiscovery(source Lookup, options DiscoveryOptions) (*Discovery, error) {
	if source == nil || !validService(options.Service) || options.Role > 2 || options.RefreshInterval <= 0 || options.RequestTimeout <= 0 || options.MaxAge < options.RefreshInterval || options.Selection.Strategy > Affinity || options.Selection.BlacklistTimeout < 0 {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Discovery{source: source, options: options, ctx: ctx, cancel: cancel, done: make(chan struct{}), changed: make(chan struct{})}, nil
}

// Get waits within the caller's connection budget; last good data has a hard MaxAge.
func (d *Discovery) Get(ctx context.Context) (*Selector, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil, 0, ErrClosed
	}
	if d.routes != nil && time.Since(d.fetched) < d.options.MaxAge {
		routes, count := d.routes, d.count
		d.mu.Unlock()
		return routes, count, nil
	}
	if d.failed {
		d.mu.Unlock()
		return nil, 0, ErrUnavailable
	}
	changed := d.changed
	if !d.started {
		d.started = true
		go d.run()
	}
	d.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	case <-d.ctx.Done():
		return nil, 0, ErrClosed
	case <-changed:
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, 0, ErrClosed
	}
	if d.routes != nil && time.Since(d.fetched) < d.options.MaxAge {
		return d.routes, d.count, nil
	}
	return nil, 0, ErrUnavailable
}

func (d *Discovery) run() {
	defer close(d.done)
	ticker := time.NewTicker(d.options.RefreshInterval)
	defer ticker.Stop()
	for {
		if d.ctx.Err() != nil {
			return
		}
		d.refresh()
		select {
		case <-d.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *Discovery) refresh() {
	ctx, cancel := context.WithTimeout(d.ctx, d.options.RequestTimeout)
	s, err := d.source(ctx)
	if err == nil {
		err = ctx.Err()
	}
	cancel()
	if err == nil {
		s, err = validateSnapshot(s, d.options.Service, d.options.Role)
	}
	var next *Selector
	count := 0
	if err == nil {
		var addresses []Address
		for _, e := range s.Endpoints {
			if d.options.Role == 0 || e.Role == d.options.Role {
				addresses = append(addresses, e.Address)
			}
		}
		count = len(addresses)
		next, err = NewSelector(addresses, d.options.Selection)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	d.failed = err != nil
	if err == nil {
		if d.routes != nil {
			next.shareState(d.routes)
		}
		d.routes, d.count, d.fetched = next, count, time.Now()
	}
	close(d.changed)
	d.changed = make(chan struct{})
}

func (s *Selector) shareState(previous *Selector) {
	state := previous.state
	state.mu.Lock()
	defer state.mu.Unlock()
	retained := s.state.blocked
	for addr := range retained {
		retained[addr] = state.blocked[addr]
	}
	state.blocked = retained
	s.state = state
}

// Close cancels only discovery. Misbehaving custom sources cannot block it indefinitely.
func (d *Discovery) Close() error {
	d.mu.Lock()
	d.closed = true
	d.cancel()
	started := d.started
	d.mu.Unlock()
	if !started {
		return nil
	}
	timer := time.NewTimer(d.options.RequestTimeout)
	defer timer.Stop()
	select {
	case <-d.done:
		return nil
	case <-timer.C:
		return context.DeadlineExceeded
	}
}
