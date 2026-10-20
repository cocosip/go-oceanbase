package valuecodec

import (
	"fmt"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// ComplexLOBColumn derives locator metadata from a complex attribute's declared type.
func ComplexLOBColumn(typ string, options config.SessionOptions) (wire.Column, error) {
	column := wire.Column{Type: 210, Charset: 63}
	if typ != valueformat.TypeNameBlob {
		column.Type = 211
	}
	if typ == valueformat.TypeNameNClob {
		name := ComplexCharset(typ, options)
		if name == charsetUTF16 {
			column.Charset = 54
		} else {
			id, ok := charset.HandshakeID(name)
			if !ok {
				return column, fmt.Errorf("%w: handshake charset", dberrors.ErrUnsupported)
			}
			column.Charset = uint16(id)
		}
	}
	return column, nil
}
