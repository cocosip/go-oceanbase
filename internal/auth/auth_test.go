package auth

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"testing"
)

func TestAuthChallengeResponses(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		fn         func(string, []byte) ([]byte, error)
	}{
		{"native", "0f8b9033e0897c0a8338ebe3dea9010dda47ab56", Native},
		{"caching-sha2", "51ecd6dedbd34d5445c0a190d4f51acf0d23b94db66c91f3f789faa9193751cd", CachingSHA2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.fn("secret", []byte("12345678901234567890"))
			if err != nil || hex.EncodeToString(got) != tc.want {
				t.Fatalf("challenge response %x: %v", got, err)
			}
			if _, err = tc.fn("secret", []byte("short")); !errors.Is(err, ErrInvalidChallenge) {
				t.Fatal("accepted invalid challenge", err)
			}
			empty, err := tc.fn("", nil)
			if err != nil || len(empty) != 0 {
				t.Fatalf("empty password response: %x %v", empty, err)
			}
		})
	}
}

func TestAuthClear(t *testing.T) {
	got, err := Clear("secret")
	if err != nil || !bytes.Equal(got, []byte{'s', 'e', 'c', 'r', 'e', 't', 0}) {
		t.Fatalf("clear response: %x %v", got, err)
	}
	if _, err = Clear("invalid\x00password"); err == nil {
		t.Fatal("embedded NUL not rejected")
	}
}

func TestAuthPinnedRSA(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := EncryptPassword("secret", []byte("12345678901234567890"), &key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	decrypted, err := rsa.DecryptOAEP(sha1.New(), rand.Reader, key, ciphertext, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decrypted, []byte{0x42, 0x57, 0x50, 0x46, 0x50, 0x42, 0x37}) {
		t.Fatal("RSA password did not include terminator and challenge XOR")
	}
	if _, err = EncryptPassword("secret", []byte("12345678901234567890"), nil); err == nil {
		t.Fatal("missing pinned key accepted")
	}
	if _, err = EncryptPassword("secret", nil, &key.PublicKey); !errors.Is(err, ErrInvalidChallenge) {
		t.Fatal("empty RSA challenge accepted", err)
	}
}
