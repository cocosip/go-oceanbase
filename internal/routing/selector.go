package routing

import (
	"math/rand/v2"
	"slices"
	"sync"
	"time"
)

// Strategy orders members within each priority group.
type Strategy uint8

// Ordering strategies preserve group priority and never duplicate a candidate.
const (
	Ordered Strategy = iota
	Random
	RoundRobin
	Affinity
)

// Options is immutable selector configuration.
type Options struct {
	Strategy         Strategy
	BlacklistTimeout time.Duration
}

// Selector owns connector-local health and concurrent round-robin state.
type Selector struct {
	addresses []Address
	options   Options
	state     *selectionState
}

// selectionState is shared by discovery generations so late dial results remain visible.
type selectionState struct {
	mu      sync.Mutex
	next    uint64
	blocked map[string]time.Time
}

// NewSelector snapshots a bounded, unique candidate list without network activity.
func NewSelector(addresses []Address, options Options) (*Selector, error) {
	if len(addresses) == 0 || len(addresses) > 1024 || options.Strategy > Affinity || options.BlacklistTimeout < 0 {
		return nil, ErrInvalid
	}
	seen := make(map[string]time.Time, len(addresses))
	for _, address := range addresses {
		if address.Addr == "" {
			return nil, ErrInvalid
		}
		if _, ok := seen[address.Addr]; ok {
			return nil, ErrInvalid
		}
		seen[address.Addr] = time.Time{}
	}
	return &Selector{addresses: slices.Clone(addresses), options: options, state: &selectionState{blocked: seen}}, nil
}

// Order returns healthy candidates or the delay until the first candidate leaves cooldown.
func (s *Selector) Order(now time.Time) ([]Address, time.Duration) {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	var result []Address
	var earliest time.Time
	for _, address := range s.addresses {
		until := s.state.blocked[address.Addr]
		if !until.After(now) {
			result = append(result, address)
		} else if earliest.IsZero() || until.Before(earliest) {
			earliest = until
		}
	}
	if len(result) == 0 {
		return nil, max(earliest.Sub(now), time.Duration(0))
	}
	slices.SortStableFunc(result, func(a, b Address) int { return int(a.Group) - int(b.Group) })
	for start := 0; start < len(result); {
		end := start + 1
		for end < len(result) && result[end].Group == result[start].Group {
			end++
		}
		s.orderGroup(result[start:end])
		start = end
	}
	s.state.next++
	return result, 0
}

func (s *Selector) orderGroup(group []Address) {
	switch s.options.Strategy {
	case Random:
		rand.Shuffle(len(group), func(i, j int) { group[i], group[j] = group[j], group[i] })
	case RoundRobin:
		n := int(s.state.next % uint64(len(group)))
		copy(group, append(slices.Clone(group[n:]), group[:n]...))
	case Affinity:
		for i := range group {
			var total uint64
			for _, a := range group[i:] {
				total += max(uint64(a.Weight), 1)
			}
			pick := rand.Uint64N(total)
			for j := i; j < len(group); j++ {
				weight := max(uint64(group[j].Weight), 1)
				if pick < weight {
					group[i], group[j] = group[j], group[i]
					break
				}
				pick -= weight
			}
		}
	}
}
