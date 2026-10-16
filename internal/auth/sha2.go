package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"errors"
)

// CachingSHA2 creates the initial caching_sha2_password response.
func CachingSHA2(password string, challenge []byte) ([]byte, error) {
	if password == "" {
		return nil, nil
	}
	if len(challenge) != 20 {
		return nil, ErrInvalidChallenge
	}
	first := sha256.Sum256([]byte(password))
	second := sha256.Sum256(first[:])
	var input [52]byte
	copy(input[:32], second[:])
	copy(input[32:], challenge)
	third := sha256.Sum256(input[:])
	for i := range first {
		first[i] ^= third[i]
	}
	return first[:], nil
}

// EncryptPassword encrypts a challenge-masked password using a caller-pinned RSA key.
func EncryptPassword(password string, challenge []byte, key *rsa.PublicKey) ([]byte, error) {
	if len(challenge) != 20 {
		return nil, ErrInvalidChallenge
	}
	if key == nil || key.N == nil || key.E < 3 {
		return nil, errors.New("auth: missing or invalid pinned RSA key")
	}
	plain, err := Clear(password)
	if err != nil {
		return nil, err
	}
	for i := range plain {
		plain[i] ^= challenge[i%len(challenge)]
	}
	return rsa.EncryptOAEP(sha1.New(), rand.Reader, key, plain, nil)
}
