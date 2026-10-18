package auth

import (
	"crypto/sha512"

	"filippo.io/edwards25519"
)

// Ed25519 signs the 32-byte challenge using the client's password-derived scalar.
// Standard ed25519.Sign rehashes a seed and does not implement client_ed25519.
//
// The protocol fixes SHA-512 here: the scalar is clamped from SHA512(password)[:32]
// and the nonce seed is SHA512(password)[32:], so the digest is part of the
// authentication algorithm rather than a password-storage choice. CodeQL's
// go/weak-sensitive-data-hashing flags it as a false positive for that reason.
func Ed25519(password string, challenge []byte) ([]byte, error) {
	if password == "" {
		return nil, nil
	}
	if len(challenge) != 32 {
		return nil, ErrInvalidChallenge
	}
	digest := sha512.Sum512([]byte(password))
	secret, _ := edwards25519.NewScalar().SetBytesWithClamping(digest[:32])
	public := new(edwards25519.Point).ScalarBaseMult(secret).Bytes()
	var input [96]byte
	copy(input[:32], digest[32:])
	copy(input[32:64], challenge)
	rHash := sha512.Sum512(input[:64])
	r, _ := edwards25519.NewScalar().SetUniformBytes(rHash[:])
	R := new(edwards25519.Point).ScalarBaseMult(r).Bytes()
	copy(input[:32], R)
	copy(input[32:64], public)
	copy(input[64:], challenge)
	kHash := sha512.Sum512(input[:])
	k, _ := edwards25519.NewScalar().SetUniformBytes(kHash[:])
	S := edwards25519.NewScalar().MultiplyAdd(k, secret, r)
	return append(R, S.Bytes()...), nil
}
