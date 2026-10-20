package charset

import "strings"

// HandshakeID maps connection encodings to their one-byte OceanBase collation.
func HandshakeID(name string) (byte, bool) {
	switch strings.ToLower(name) {
	case nameUTF8MB4:
		return 45, true
	case "utf8", nameUTF8MB3:
		return 33, true
	case nameLatin1:
		return 8, true
	case nameGBK:
		return 28, true
	case nameGB18030:
		return 248, true
	case nameHKSCS:
		return 152, true
	case nameHKSCS31:
		return 153, true
	case nameGB180302022:
		return 217, true
	case nameASCII:
		return 11, true
	case nameBinary:
		return 63, true
	default:
		return 0, false
	}
}
