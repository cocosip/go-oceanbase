package oceanbase

import (
	driver "database/sql/driver"

	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
)

const readySQL = "SET @ready=1"

var peerDial = testpeer.PeerDial

var serveConnect = testpeer.ServeConnect

var serveOK = testpeer.ServeOK

var serveBaseline = testpeer.ServeBaseline

var serveQuit = testpeer.ServeQuit

var _ driver.Connector = (*connector)(nil)
