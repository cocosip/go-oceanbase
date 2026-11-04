//go:build integration

package integrationtest

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestReceiptLossRequiresCompleteOK(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		ok   bool
	}{
		{"complete", []byte{7, 0, 0, 1, 0, 0, 0, 2, 0, 0, 0}, true},
		{"short-body", []byte{7, 0, 0, 1, 0, 0}, false},
		{"short-header", []byte{7, 0}, false},
		{"wrong-sequence", []byte{7, 0, 0, 2, 0, 0, 0, 2, 0, 0, 0}, false},
		{"server-error", []byte{7, 0, 0, 1, 255, 0, 0, 2, 0, 0, 0}, false},
		{"more-results", []byte{7, 0, 0, 1, 0, 0, 0, 10, 0, 0, 0}, false},
		{"oversized", []byte{1, 0, 1, 1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer func() { _ = client.Close() }()
			if err := client.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer func() { _ = server.Close() }()
				for _, b := range tc.data {
					if _, err := server.Write([]byte{b}); err != nil {
						return
					}
				}
			}()
			fault := &ReceiptLossConn{Conn: client}
			fault.Arm()
			buf := make([]byte, 4)
			n, err := fault.Read(buf)
			if n != 0 || err == nil || fault.Acknowledged() != tc.ok || tc.ok && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatal("receipt evidence mismatch", n, err, fault.Acknowledged())
			}
			<-done
		})
	}
}
