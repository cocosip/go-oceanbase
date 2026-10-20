// Package valuecodec implements pure OceanBase value codecs.
package valuecodec

import (
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// Key identifies a type in the physical session's compatibility mode.
type Key struct {
	Mode         config.Mode
	Schema, Name string
	Version      uint64
}

// Descriptor is built by catalog and remains immutable after publication.
type Descriptor struct {
	Key    Key
	Array  bool
	Fields []Field
	Shape  *wire.ComplexShape
}

// Field describes one object attribute or a collection element.
type Field struct {
	Name, Type string
	Child      *Descriptor
}
