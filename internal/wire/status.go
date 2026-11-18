package wire

// Extended column type codes reported by the Oracle-compatible protocol.
const (
	TypeRefCursor = 163
	TypeBlob      = 210
	TypeClob      = 211
)

// Server status flags carried by OK and EOF packets.
const (
	StatusAutocommit         = 1 << 1
	StatusMoreResults        = 1 << 3
	StatusCursorExists       = 1 << 6
	StatusLastRowSent        = 1 << 7
	StatusNoBackslashEscapes = 1 << 9
	StatusPSOutParameters    = 1 << 12
	StatusInTransaction      = 1 << 13
)
