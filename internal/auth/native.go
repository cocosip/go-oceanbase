package auth

import "crypto/sha1"

// Native creates a mysql_native_password challenge response.
//
// The wire protocol fixes the algorithm: the server sends a seed and expects
// SHA1(password) XOR SHA1(seed || SHA1(SHA1(password))). This is a challenge
// response, not password storage, and a salted or memory-hard hash cannot be
// substituted without the server rejecting the handshake. CodeQL's
// go/weak-sensitive-data-hashing flags it as a false positive for that reason.
func Native(password string, challenge []byte) ([]byte, error) {
	if password == "" {
		return nil, nil
	}
	if len(challenge) != 20 {
		return nil, ErrInvalidChallenge
	}
	first := sha1.Sum([]byte(password))
	second := sha1.Sum(first[:])
	var input [40]byte
	copy(input[:20], challenge)
	copy(input[20:], second[:])
	third := sha1.Sum(input[:])
	for i := range first {
		first[i] ^= third[i]
	}
	return first[:], nil
}
