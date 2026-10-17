//go:build windows

package auth

import (
	"bytes"
	"errors"
	"syscall"
	"testing"
)

func TestAuthPluginGSSNativeSSPI(t *testing.T) {
	c, err := NewGSSContext("service/localhost", "NTLM")
	if errors.Is(err, syscall.Errno(0x8009030e)) { // SEC_E_NO_CREDENTIALS
		t.Skip("this process has no Windows SSPI identity credentials")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if e := c.Close(); e != nil {
			t.Error(e)
		}
	})
	token, complete, err := c.Step(nil)
	if err != nil || complete || !bytes.HasPrefix(token, []byte("NTLMSSP\x00")) {
		t.Fatalf("native SSPI initial token: complete=%v: %v", complete, err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err = c.Step(nil); err == nil {
		t.Fatal("closed SSPI context reused")
	}
}
