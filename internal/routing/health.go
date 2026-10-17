package routing

import "time"

// Failure starts cooldown only for a configured candidate, keeping state bounded.
func (s *Selector) Failure(addr string, now time.Time) {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if _, ok := s.state.blocked[addr]; ok {
		s.state.blocked[addr] = now.Add(s.options.BlacklistTimeout)
	}
}

// Success clears previous cooldown without changing any established connection.
func (s *Selector) Success(addr string) {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if _, ok := s.state.blocked[addr]; ok {
		s.state.blocked[addr] = time.Time{}
	}
}
