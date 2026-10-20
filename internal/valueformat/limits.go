package valueformat

import (
	"fmt"

	"github.com/cocosip/go-oceanbase/dberrors"
)

// Shared complex-value recursion and payload limits.
const (
	MaxComplexDepth = 32
	MaxComplexNodes = 65536
	MaxComplexJSON  = 16 << 20
)

// ComplexNode consumes one node from a shared recursive value budget.
func ComplexNode(depth int, nodes *int) error {
	if depth > MaxComplexDepth || *nodes <= 0 {
		return fmt.Errorf("%w: nesting or node limit", dberrors.ErrComplex)
	}
	*nodes--
	return nil
}
