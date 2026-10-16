package wire

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"
)

// Independently constructed protocol-v10 greeting, synthetic user and scramble.
func helloFixture(t *testing.T) []byte {
	t.Helper()
	data, err := hex.DecodeString("0a352e372e32352d4f6365616e42617365000403020131323334353637380005822d020028001500000000000000000000393031323334353637383930006d7973716c5f6e61746976655f70617373776f726400")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestHandshakeDecode(t *testing.T) {
	data := helloFixture(t)
	h, err := DecodeHandshake(data)
	if err != nil {
		t.Fatal(err)
	}
	if h.ServerVersion != "5.7.25-OceanBase" || h.ConnectionID != 0x01020304 || h.Charset != 45 || h.Status != 2 || h.Capabilities != 0x288205 || h.Plugin != "mysql_native_password" || !bytes.Equal(h.Challenge, []byte("12345678901234567890")) {
		t.Fatalf("incorrect handshake: %+v", h)
	}
	for _, size := range []int{0, 1, 12, 25, 40, 50} {
		if _, err = DecodeHandshake(data[:size]); !errors.Is(err, ErrMalformed) {
			t.Fatalf("accepted truncated greeting (%d): %v", size, err)
		}
	}
	data[0] = 9
	if _, err = DecodeHandshake(data); !errors.Is(err, ErrMalformed) {
		t.Fatal("unsupported protocol accepted", err)
	}
}

func TestHandshakeEncode(t *testing.T) {
	response, err := EncodeHandshakeResponse(HandshakeResponse{Capabilities: 0x00088208, MaxPacketBytes: 64 << 20, Charset: 45, User: "app", Database: "db", Plugin: "mysql_native_password", Auth: []byte{1, 2}})
	if err != nil {
		t.Fatal(err)
	}
	want, err := hex.DecodeString("08820800000000042d0000000000000000000000000000000000000000000000617070000201026462006d7973716c5f6e61746976655f70617373776f726400")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(response, want) {
		t.Fatalf("handshake encoding\n got %x\nwant %x", response, want)
	}
	if _, err = EncodeHandshakeResponse(HandshakeResponse{User: "app\x00bad"}); !errors.Is(err, ErrMalformed) {
		t.Fatal("NUL username accepted", err)
	}
	if _, err = EncodeHandshakeResponse(HandshakeResponse{Capabilities: ClientSecureConnection, Auth: make([]byte, 256)}); !errors.Is(err, ErrMalformed) {
		t.Fatal("single-byte auth length overflow accepted", err)
	}
}

func TestAuthOK(t *testing.T) {
	ok, err := DecodeOK([]byte{0, 0, 0, 6, 0, 0, 0}, ClientProtocol41)
	if err != nil || ok.Status != 6 || ok.AffectedRows != 0 || ok.InsertID != 0 {
		t.Fatalf("auth OK: %+v %v", ok, err)
	}
	for _, data := range [][]byte{{0}, {0, 251, 0, 0, 0, 0, 0}, {0, 254, 1}, {0, 0, 0, 0}} {
		if _, err = DecodeOK(data, ClientProtocol41); !errors.Is(err, ErrMalformed) {
			t.Fatalf("accepted malformed OK %x: %v", data, err)
		}
	}
	tracked := []byte{0, 0, 0, 2, 64, 0, 0, 1, 'i', 2, 0, 0}
	ok, err = DecodeOK(tracked, ClientProtocol41|ClientSessionTrack)
	if err != nil || string(ok.Info) != "i" || !bytes.Equal(ok.State, []byte{0, 0}) {
		t.Fatalf("session-tracked OK: %+v %v", ok, err)
	}
}

func TestAuthOKMissingSessionState(t *testing.T) {
	for _, data := range [][]byte{{0, 0, 0, 2, 64, 0, 0}, {0, 0, 0, 2, 64, 0, 0, 0}} {
		if _, err := DecodeOK(data, ClientProtocol41|ClientSessionTrack); !errors.Is(err, ErrMalformed) {
			t.Fatalf("missing changed session state accepted: %v", err)
		}
	}
}

func FuzzHandshake(f *testing.F) {
	f.Add([]byte{10, 0})
	f.Add([]byte{0, 0, 0, 2, 0, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4096 {
			t.Skip()
		}
		_, _ = DecodeHandshake(data)
		_, _ = DecodeOK(data, ClientProtocol41|ClientSessionTrack)
	})
}
