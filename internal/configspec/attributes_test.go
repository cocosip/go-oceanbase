package configspec

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestAttributesEncodedBudget(t *testing.T) {
	const charsetUTF8MB4 = "utf8mb4"
	for _, tc := range []struct {
		charset, value, encoded string
	}{
		{"gbk", strings.Repeat("测", 150), strings.Repeat("\xb2\xe2", 150)},
		{"gb18030", strings.Repeat("\u0080", 100), strings.Repeat("\x81\x30\x81\x30", 100)},
		{charsetUTF8MB4, strings.Repeat("a", 250), strings.Repeat("a", 250)},
		{charsetUTF8MB4, strings.Repeat("a", 251), strings.Repeat("a", 251)},
		{charsetUTF8MB4, strings.Repeat("a", 65535), strings.Repeat("a", 65535)},
		{charsetUTF8MB4, strings.Repeat("a", 65536), strings.Repeat("a", 65536)},
	} {
		t.Run(fmt.Sprintf("%s/%d", tc.charset, len(tc.value)), func(t *testing.T) {
			packet, err := wire.EncodeHandshakeResponse(wire.HandshakeResponse{Capabilities: wire.ClientConnectAttrs, Attributes: map[string]string{
				"__mysql_client_type":                   "__ob_jdbc_client",
				"application":                           tc.encoded,
				"__ob_client_attribute_capability_flag": "3",
				"__proxy_capability_flag":               "1360200",
			}})
			if err != nil {
				t.Fatal(err)
			}
			limit := int64(len(packet) - 34)
			base := ProtocolAttributes(true, true, true, false, true)
			configured := map[string]string{"application": tc.value}
			attrs, field := ConnectionAttributes(base, configured, "", tc.charset, false, limit)
			if field != "" || attrs["application"] != tc.encoded {
				t.Fatalf("exact wire budget rejected or encoding changed: %s", field)
			}
			if _, field = ConnectionAttributes(base, configured, "", tc.charset, false, limit-1); field != "ConnectionAttributes" {
				t.Fatal("encoded bytes or length prefixes were omitted from the budget")
			}
			if _, changed := base["application"]; changed {
				t.Fatal("attribute construction mutated the input map")
			}
		})
	}
}
