package auth

// OldPassword encodes the legacy eight-byte scramble and its trailing NUL.
// This weak algorithm requires explicit opt-in at the driver boundary.
func OldPassword(password string, challenge []byte) ([]byte, error) {
	if password == "" {
		return nil, nil
	}
	if len(challenge) < 8 {
		return nil, ErrInvalidChallenge
	}
	a, b := oldHash([]byte(password))
	x, y := oldHash(challenge[:8])
	const modulus uint64 = 0x3fffffff
	x, y = (x^a)%modulus, (y^b)%modulus
	next := func() byte {
		x = (3*x + y) % modulus
		y = (x + y + 33) % modulus
		return byte(x * 31 / modulus)
	}
	result := make([]byte, 9)
	for i := range 8 {
		result[i] = next() + 64
	}
	extra := next()
	for i := range 8 {
		result[i] ^= extra
	}
	return result, nil
}

func oldHash(data []byte) (uint64, uint64) {
	a, b, add := uint64(1345345333), uint64(0x12345671), uint64(7)
	for _, c := range data {
		if c == ' ' || c == '\t' {
			continue
		}
		a ^= ((a&63)+add)*uint64(c) + (a << 8)
		b += (b << 8) ^ a
		add += uint64(c)
	}
	return a & 0x7fffffff, b & 0x7fffffff
}
