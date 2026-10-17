package routing

import "time"

// Failure starts cooldown only for a configured candidate, keeping state bounded.
func (s *Selector) Failure(addr string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.blocked[addr]; ok {
		s.blocked[addr] = now.Add(s.options.BlacklistTimeout)
	}
}

// Success clears previous cooldown without changing any established connection.
func (s *Selector) Success(addr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.blocked[addr]; ok {
		s.blocked[addr] = time.Time{}
	}
}
