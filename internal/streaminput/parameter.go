package streaminput

import (
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// Parameter validates a streamed input without reading or claiming its source.
func Parameter(source *Source, mode config.Mode, oracleSupported bool, maxLOBBytes, maxMessageBytes int64) (wire.Parameter, error) {
	if err := source.Validate(); err != nil {
		return wire.Parameter{}, err
	}
	limit := source.MaxBytes
	if limit == 0 {
		limit = maxLOBBytes
	}
	if source.Length > limit {
		return wire.Parameter{}, wire.ErrMessageTooLarge
	}
	typ := uint16(252)
	if source.Text {
		typ = 253
	}
	if mode == config.ModeOracle {
		if !oracleSupported {
			return wire.Parameter{}, dberrors.ErrUnsupported
		}
		typ = 254
	}
	if maxMessageBytes < 25 || source.Text && ChunkSize(mode, source.Text, maxMessageBytes) < utf8.UTFMax {
		return wire.Parameter{}, wire.ErrMessageTooLarge
	}
	return wire.Parameter{Type: typ, LongData: true}, nil
}
