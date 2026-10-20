package connect

import (
	"github.com/cocosip/go-oceanbase/internal/testpeer"
)

const (
	testUser                      = "app"
	testPassword                  = "secret"
	testServerName                = "db.example"
	testCustomPlugin              = "custom"
	testAttributeKey              = "application"
	testAttributeValue            = "billing,worker:1"
	testAttributeGB18030          = "gb18030"
	localFilenameCharset          = "gbk"
	testAttributeGBKValue         = "\xb2\xe2\xca\xd4"
	testClientAttributeCapability = "__ob_client_attribute_capability_flag"
)

var sendPackets = testpeer.SendPackets

func commandPacket(affected, insertID, status byte) []byte {
	return []byte{0, affected, insertID, status, 0, 0, 0}
}

var readAuthResponse = testpeer.ReadAuthResponse
var protocolGreeting = testpeer.ProtocolGreeting
var protocolOK = testpeer.ProtocolOK

const testCharsetUTF8MB4 = "utf8mb4"

const testSessionQuotedNoBackslash = "'x\\''y'"
