package oceanbase

import (
	"bytes"
	"slices"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func (s *stmt) parameterOptions(query []byte, params []wire.Parameter, combined bool) wire.ExecuteOptions {
	o := s.conn.executeOptions(query, combined)
	if !s.conn.cfg.Execution.ReuseParameterTypes {
		return o
	}
	o.ReuseTypes = s.id != 0 && sameParameterTypes(params, s.lastTypes)
	s.pendingTypes = make([]wire.Parameter, len(params))
	for i, p := range params {
		s.pendingTypes[i] = wire.Parameter{Type: p.Type, TypeMetadata: slices.Clone(p.TypeMetadata)}
	}
	return o
}

func sameParameterTypes(a, b []wire.Parameter) bool {
	if len(a) != len(b) {
		return false
	}
	for i, p := range a {
		if p.Type != b[i].Type || !bytes.Equal(p.TypeMetadata, b[i].TypeMetadata) {
			return false
		}
	}
	return true
}
