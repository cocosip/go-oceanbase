package wire

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// OB_TEST_JDBC_JAR enables an independent reference; ordinary tests need no Java.
func TestCompressionJavaInterop(t *testing.T) {
	jar := os.Getenv("OB_TEST_JDBC_JAR")
	if jar == "" {
		t.Skip("set OB_TEST_JDBC_JAR to official oceanbase-client-2.4.14.jar")
	}
	jar, err := filepath.Abs(jar)
	if err != nil {
		t.Fatal(err)
	}
	java := os.Getenv("OB_TEST_JAVA")
	if java == "" {
		java = "java"
	}
	random := rand.New(rand.NewPCG(1, 2))
	noise := make([]byte, 4096)
	for i := range noise {
		noise[i] = byte(random.UintN(256))
	}
	limitedSaving := bytes.Clone(noise[:1024])
	copy(limitedSaving[944:], limitedSaving[:80])
	cases := map[string][]byte{
		"empty-message":  {},
		"100-bytes":      bytes.Repeat([]byte{'a'}, 100),
		"101-bytes":      bytes.Repeat([]byte{'a'}, 101),
		"limited-saving": limitedSaving,
		"noise":          noise,
		"unicode":        bytes.Repeat([]byte("OceanBase 中文 😀"), 256),
		"exact-max":      bytes.Repeat([]byte{'a'}, maxFrame),
		"over-max":       bytes.Repeat([]byte{'b'}, maxFrame+1),
	}
	dir := t.TempDir()
	for name, payload := range cases {
		transport := newBufferConn(nil)
		p := NewPacketIO(transport, 32<<20)
		if err := p.EnableCompression(); err != nil {
			t.Fatal(err)
		}
		if err := p.WriteMessage(context.Background(), payload); err != nil {
			t.Fatal(err)
		}
		// Java's reader needs lookahead to recognize an empty native packet.
		if err := p.WriteMessage(context.Background(), []byte{0x42}); err != nil {
			t.Fatal(err)
		}
		for suffix, data := range map[string][]byte{".payload": payload, ".go": transport.out.Bytes()} {
			if err := os.WriteFile(filepath.Join(dir, name+suffix), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, java, "-Xmx512m", "-cp", jar, filepath.Join("testdata", "CompressionInterop.java"), dir)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("official JDBC interop: %v\n%s", err, output)
	} else {
		t.Log(string(output))
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(dir, name+".java"))
			if err != nil {
				t.Fatal(err)
			}
			p := NewPacketIO(newBufferConn(data), 32<<20)
			if err := p.EnableCompression(); err != nil {
				t.Fatal(err)
			}
			got, err := p.ReadMessage(context.Background())
			// Official 2.4.14 advances the inner sequence twice on zlib-to-raw
			// fallback and omits trailing bytes at the exact native packet limit.
			var wantErr error
			switch name {
			case "limited-saving", "noise":
				wantErr = ErrSequence
			case "exact-max":
				wantErr = io.EOF
			}
			if wantErr != nil {
				if !errors.Is(err, wantErr) {
					t.Fatalf("expected rejection of known JDBC 2.4.14 defect: got %v, want %v", err, wantErr)
				}
				t.Logf("known JDBC 2.4.14 output defect rejected: %v", err)
				return
			}
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("Java encode -> Go decode: bytes=%d, want=%d, err=%v", len(got), len(want), err)
			}
			if len(p.frames.buffer) != 0 || p.frames.conn.(*bufferConn).in.Len() != 0 {
				t.Fatal("Java frame data left unread")
			}
		})
	}
}
