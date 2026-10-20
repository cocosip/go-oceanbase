package valuecodec

import "github.com/cocosip/go-oceanbase/internal/wire"

// ParamSet contains encoded physical parameters, without input readers or bindings.
type ParamSet []wire.Parameter
