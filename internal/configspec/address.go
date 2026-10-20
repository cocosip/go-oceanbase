package configspec

import (
	"github.com/cocosip/go-oceanbase/internal/routing"
)

// NormalizeAddress validates and canonicalizes a TCP endpoint with its default port.
func NormalizeAddress(addr string) (string, error) {
	return routing.NormalizeAddress(addr)
}
