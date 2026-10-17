package auth

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"
)

// Published interoperability vectors from go-sql-driver/mysql v1.9.3 auth_test.go.
func TestAuthPluginAlgorithms(t *testing.T) {
	t.Run("old", func(t *testing.T) {
		for _, tc := range []struct{ password, want string }{
			{" pass", "47575c5a435b425100"}, {"pass ", "47575c5a435b425100"},
			{"123\t456", "575c47505b5b555900"}, {"C0mpl!ca ted#PASS123", "5d5d554849584a4500"},
		} {
			got, err := OldPassword(tc.password, []byte{9, 8, 7, 6, 5, 4, 3, 2})
			if err != nil || hex.EncodeToString(got) != tc.want {
				t.Fatalf("old response %x: %v", got, err)
			}
		}
		if _, err := OldPassword("x", []byte("short")); !errors.Is(err, ErrInvalidChallenge) {
			t.Fatal(err)
		}
		if got, err := OldPassword("", nil); err != nil || len(got) != 0 {
			t.Fatalf("empty: %x %v", got, err)
		}
	})
	t.Run("ed25519", func(t *testing.T) {
		want := []byte{232, 61, 201, 63, 67, 63, 51, 53, 86, 73, 238, 35, 170, 117, 146, 214, 26, 17, 35, 9, 8, 132, 245, 141, 48, 99, 66, 58, 36, 228, 48, 84, 115, 254, 187, 168, 88, 162, 249, 57, 35, 85, 79, 238, 167, 106, 68, 117, 56, 135, 171, 47, 20, 14, 133, 79, 15, 229, 124, 160, 176, 100, 138, 14}
		got, err := Ed25519("foobar", bytes.Repeat([]byte{'A'}, 32))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("ed25519 response %x: %v", got, err)
		}
		for _, size := range []int{0, 20, 31, 33} {
			if _, err = Ed25519("x", make([]byte, size)); !errors.Is(err, ErrInvalidChallenge) {
				t.Fatalf("length %d: %v", size, err)
			}
		}
		if got, err = Ed25519("", nil); err != nil || len(got) != 0 {
			t.Fatalf("empty: %x %v", got, err)
		}
	})
}

func TestAuthPluginPAMPrompt(t *testing.T) {
	for _, flag := range []byte{2, 3, 4, 5} {
		text, echo, last, err := PAMPrompt([]byte{flag, 'x', 0})
		if err != nil || text != "x" || echo != (flag < 4) || last != (flag%2 == 1) {
			t.Fatalf("flag %d: %q %v %v %v", flag, text, echo, last, err)
		}
	}
	for _, bad := range [][]byte{nil, {1}, {6}, {4, 0, 'x'}, {4, 0xff}} {
		if _, _, _, err := PAMPrompt(bad); !errors.Is(err, ErrInvalidChallenge) {
			t.Fatalf("accepted malformed prompt %x", bad)
		}
	}
}

func FuzzAuthPlugins(f *testing.F) {
	f.Add("foobar", []byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"))
	f.Add("", []byte{4, 'x', 0})
	f.Add("pinned/service", []byte("service/db.example\x00Negotiate\x00"))
	f.Fuzz(func(t *testing.T, password string, challenge []byte) {
		if len(password) > 4096 || len(challenge) > 4096 {
			t.Skip()
		}
		for _, algorithm := range []func(string, []byte) ([]byte, error){OldPassword, Ed25519} {
			got, err := algorithm(password, challenge)
			if err == nil && len(got) != 0 && len(got) != 9 && len(got) != 64 {
				t.Fatalf("invalid response length %d", len(got))
			}
		}
		_, _, _, _ = PAMPrompt(challenge)
		spn, mechanism, err := GSSAPISeed(challenge, password)
		if err == nil {
			if !ValidGSSPrincipal(spn) || (password != "" && spn != password) {
				t.Fatal("invalid GSS principal or override")
			}
			canonical := []byte(spn + "\x00" + mechanism + "\x00")
			again, selected, e := GSSAPISeed(canonical, "")
			if e != nil || again != spn || selected != mechanism {
				t.Fatal("GSS seed did not round trip")
			}
		}
	})
}
