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
	case nameSJIS:
		return 13, true
	case nameBig5:
		return 1, true
	case nameGB2312:
		return 24, true
	case nameEUCKR:
		return 19, true
	case nameDEC8:
		return 3, true
	case nameTIS620:
		return 18, true
	case nameCP850:
		return 4, true
	case nameHP8:
		return 6, true
	case nameMacRoman:
		return 39, true
	case nameSWE7:
		return 10, true
	case nameUJIS:
		return 12, true
	case nameCP932:
		return 95, true
	case nameEUCJPMS:
		return 97, true
	default:
		return 0, false
	}
}
