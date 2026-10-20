package materialize_test

import (
	"github.com/cocosip/go-oceanbase/internal/testpeer"
)

func complexColumnFixture(version byte) []byte {
	b := testpeer.TextColumn("v", 160, 45, 0)
	return append(b, 1, 'S', 1, 'T', version)
}
