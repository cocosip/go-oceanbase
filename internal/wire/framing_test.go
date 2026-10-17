package wire

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/testutil"
)

func compressedFixture(t testing.TB, payload []byte, seq byte, compress bool) []byte {
	t.Helper()
	raw := payload
	expanded := 0
	if compress {
		var b bytes.Buffer
		w := zlib.NewWriter(&b)
		if _, err := w.Write(payload); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		raw = b.Bytes()
		expanded = len(payload)
	}
	n := len(raw)
	return append([]byte{byte(n), byte(n >> 8), byte(n >> 16), seq, byte(expanded), byte(expanded >> 8), byte(expanded >> 16)}, raw...)
}

func TestCompressedPackets(t *testing.T) {
	standard := []byte{3, 0, 0, 0, 'o', 'n', 'e', 3, 0, 0, 1, 't', 'w', 'o'}
	for _, compress := range []bool{false, true} {
		for _, split := range []int{2, len(standard)} {
			data := compressedFixture(t, standard[:split], 0, compress)
			if split < len(standard) {
				data = append(data, compressedFixture(t, standard[split:], 1, compress)...)
			}
			p := NewPacketIO(newBufferConn(data), 64)
			if err := p.EnableCompression(); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"one", "two"} {
				got, err := p.ReadMessage(context.Background())
				if err != nil || string(got) != want {
					t.Fatal(got, err)
				}
			}
		}
	}
	for _, data := range [][]byte{
		compressedFixture(t, standard, 1, true),
		{1, 0, 0, 0, 255, 255, 127, 0},
		{1, 0, 0, 0, 5, 0, 0, 0},
	} {
		p := NewPacketIO(newBufferConn(data), 64)
		_ = p.EnableCompression()
		if _, err := p.ReadMessage(context.Background()); err == nil {
			t.Fatal("invalid compression accepted")
		}
		if _, err := p.ReadMessage(context.Background()); !errors.Is(err, ErrDesynchronized) {
			t.Fatal(err)
		}
	}
}

func crc16Reference(data []byte) uint16 {
	var sum uint16
	for _, b := range data {
		sum ^= uint16(b)
		for range 8 {
			if sum&1 != 0 {
				sum = sum>>1 ^ 0xa001
			} else {
				sum >>= 1
			}
		}
	}
	return sum
}
func crc32Reference(data []byte) uint32 {
	var sum uint32
	for _, b := range data {
		sum ^= uint32(b)
		for range 8 {
			if sum&1 != 0 {
				sum = sum>>1 ^ 0x82f63b78
			} else {
				sum >>= 1
			}
		}
	}
	return sum
}
func ob20Fixture(payload []byte, seq byte, flags uint32) []byte {
	n := 24 + len(payload) + 4
	h := make([]byte, 31)
	h[0], h[1], h[2], h[3] = byte(n), byte(n>>8), byte(n>>16), seq
	binary.LittleEndian.PutUint16(h[7:], 0x20ab)
	binary.LittleEndian.PutUint16(h[9:], 20)
	binary.LittleEndian.PutUint32(h[11:], 42)
	h[15] = 1
	h[18] = seq
	binary.LittleEndian.PutUint32(h[19:], uint32(len(payload)))
	binary.LittleEndian.PutUint32(h[23:], flags)
	binary.LittleEndian.PutUint16(h[29:], crc16Reference(h[:29]))
	h = append(h, payload...)
	return binary.LittleEndian.AppendUint32(h, crc32Reference(payload))
}

func TestOB20Frames(t *testing.T) {
	standard := []byte{3, 0, 0, 0, 'o', 'n', 'e', 3, 0, 0, 1, 't', 'w', 'o'}
	extra := []byte{0xe9, 7, 3, 0, 0, 0, 1, 2, 3} // unknown, length-delimited new-extra key
	payload := binary.LittleEndian.AppendUint32(nil, uint32(len(extra)))
	payload = append(payload, extra...)
	payload = append(payload, standard[:2]...)
	data := append(ob20Fixture(payload, 0, 1|8), ob20Fixture(standard[2:], 1, 2|8)...)
	p := NewPacketIO(newBufferConn(data), 128)
	if err := p.EnableOB20(OB20Options{ConnectionID: 42, Checksum: true, NewExtra: true}); err != nil {
		t.Fatal(err)
	}
	p.ResetSequence()
	for _, want := range []string{"one", "two"} {
		b, err := p.ReadMessage(context.Background())
		if err != nil || string(b) != want {
			t.Fatal(b, err)
		}
	}
	if crc16Reference([]byte("123456789")) != 0xbb3d || crc32Reference([]byte("123456789")) != 0x58e3fa20 {
		t.Fatal("reference checksum vector")
	}
}

func TestOB20InvalidFrames(t *testing.T) {
	good := ob20Fixture([]byte{1, 0, 0, 0, 'x'}, 0, 2)
	for _, index := range []int{0, 4, 7, 9, 11, 15, 18, 19, 29, 31, len(good) - 1} {
		data := bytes.Clone(good)
		data[index] ^= 1
		if index < 29 {
			binary.LittleEndian.PutUint16(data[29:], crc16Reference(data[:29]))
		}
		p := NewPacketIO(newBufferConn(data), 64)
		_ = p.EnableOB20(OB20Options{ConnectionID: 42, Checksum: true})
		p.ResetSequence()
		if _, err := p.ReadMessage(context.Background()); err == nil {
			t.Fatal("invalid OB20 accepted", index)
		}
	}
	// A zero checksum is allowed only when verification is not required.
	data := bytes.Clone(good)
	clear(data[29:31])
	clear(data[len(data)-4:])
	optional := NewPacketIO(newBufferConn(data), 64)
	_ = optional.EnableOB20(OB20Options{ConnectionID: 42})
	optional.ResetSequence()
	if got, err := optional.ReadMessage(context.Background()); err != nil || string(got) != "x" {
		t.Fatal(got, err)
	}
	p := NewPacketIO(newBufferConn(data), 64)
	_ = p.EnableOB20(OB20Options{ConnectionID: 42, RequireChecksum: true})
	p.ResetSequence()
	if _, err := p.ReadMessage(context.Background()); err == nil {
		t.Fatal("missing required checksum")
	}
}

func TestOB20Extras(t *testing.T) {
	for _, extra := range [][]byte{{1}, {1, 0, 5, 0, 0, 0}, {1, 0, 0, 0, 0, 1}} {
		payload := binary.LittleEndian.AppendUint32(nil, uint32(len(extra)))
		payload = append(payload, extra...)
		payload = append(payload, 1, 0, 0, 0, 'x')
		p := NewPacketIO(newBufferConn(ob20Fixture(payload, 0, 1|8|2)), 64)
		_ = p.EnableOB20(OB20Options{ConnectionID: 42})
		p.ResetSequence()
		if _, err := p.ReadMessage(context.Background()); err == nil {
			t.Fatal("invalid extra field accepted")
		}
	}
}

func TestSessionVariables(t *testing.T) {
	data := []byte{9, 2, 1, 2, 0, 4, 1, 'a', 1, 'b'}
	vars, err := DecodeSessionVariables(data)
	if err != nil || len(vars) != 1 || vars["a"] != "b" {
		t.Fatal(vars, err)
	}
	for _, data := range [][]byte{{0}, {0, 9}, {0, 2, 1, 'a'}, {0, 4, 1, 'a', 1, 'b', 0, 4, 1, 'a', 1, 'c'}} {
		if _, err := DecodeSessionVariables(data); err == nil {
			t.Fatal("invalid session record accepted", data)
		}
	}
}

func TestFramedWrites(t *testing.T) {
	for _, ob := range []bool{false, true} {
		c := newBufferConn(nil)
		p := NewPacketIO(c, 32<<20)
		if ob {
			_ = p.EnableOB20(OB20Options{ConnectionID: 42, Checksum: true})
		} else {
			_ = p.EnableCompression()
		}
		p.ResetSequence()
		want := bytes.Repeat([]byte{'a'}, 0xffffff)
		if err := p.WriteMessage(context.Background(), want); err != nil {
			t.Fatal(err)
		}
		peer := NewPacketIO(newBufferConn(c.out.Bytes()), 32<<20)
		if ob {
			_ = peer.EnableOB20(OB20Options{ConnectionID: 42, RequireChecksum: true})
		} else {
			_ = peer.EnableCompression()
		}
		peer.ResetSequence()
		got, err := peer.ReadMessage(context.Background())
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal(len(got), err)
		}
		if !p.Framed() {
			t.Fatal("framing absent")
		}
	}
}

func TestCompressedTrailingData(t *testing.T) {
	data := compressedFixture(t, []byte{1, 0, 0, 0, 'x'}, 0, true)
	data = append(data, 1)
	data[0]++
	p := NewPacketIO(newBufferConn(data), 64)
	_ = p.EnableCompression()
	if _, err := p.ReadMessage(context.Background()); err == nil || errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
}

func TestFramedMessageBudget(t *testing.T) {
	for _, ob := range []bool{false, true} {
		data := append([]byte{40, 0, 0, 0}, bytes.Repeat([]byte{'a'}, 40)...)
		data = append(data, 40, 0, 0, 1)
		data = append(data, bytes.Repeat([]byte{'b'}, 40)...)
		frame := compressedFixture(t, data, 0, true)
		if ob {
			frame = ob20Fixture(data, 0, 2)
		}
		p := NewPacketIO(newBufferConn(frame), 64)
		if ob {
			_ = p.EnableOB20(OB20Options{ConnectionID: 42})
		} else {
			_ = p.EnableCompression()
		}
		p.ResetSequence()
		for _, want := range []byte{'a', 'b'} {
			got, err := p.ReadMessage(context.Background())
			if err != nil || !bytes.Equal(got, bytes.Repeat([]byte{want}, 40)) {
				t.Fatal("outer frame incorrectly charged as one logical message", ob, err)
			}
		}
		data = append([]byte{65, 0, 0, 0}, bytes.Repeat([]byte{'x'}, 65)...)
		frame = compressedFixture(t, data, 0, true)
		if ob {
			frame = ob20Fixture(data, 0, 2)
		}
		p = NewPacketIO(newBufferConn(frame), 64)
		if ob {
			_ = p.EnableOB20(OB20Options{ConnectionID: 42})
		} else {
			_ = p.EnableCompression()
		}
		p.ResetSequence()
		if _, err := p.ReadMessage(context.Background()); !errors.Is(err, ErrMessageTooLarge) {
			t.Fatal("oversized inner message accepted", err)
		}
	}
}

func TestFramedReadByteEvidence(t *testing.T) {
	frame := ob20Fixture([]byte{1, 0, 0, 0, 'x'}, 0, 2)
	frame[len(frame)-1] ^= 1
	p := NewPacketIO(newBufferConn(frame), 64)
	_ = p.EnableOB20(OB20Options{ConnectionID: 42})
	p.ResetSequence()
	_, err := p.ReadMessage(context.Background())
	var transport *IOError
	if !errors.As(err, &transport) || !errors.Is(err, ErrChecksum) || transport.Bytes != int64(len(frame)) {
		t.Fatal("physical read evidence lost", err)
	}
}

func TestFramedCancellationAndPartialWrite(t *testing.T) {
	for _, ob := range []bool{false, true} {
		client, server := net.Pipe()
		p := NewPacketIO(client, 64)
		if ob {
			_ = p.EnableOB20(OB20Options{ConnectionID: 42})
		} else {
			_ = p.EnableCompression()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_, err := p.ReadMessage(ctx)
		cancel()
		_ = client.Close()
		_ = server.Close()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		p.ResetSequence()
		if err = p.WriteMessage(context.Background(), nil); !errors.Is(err, ErrDesynchronized) {
			t.Fatal("canceled stream reused", err)
		}
		buf := newBufferConn(nil)
		fault := testutil.NewFaultConn(buf, testutil.Faults{MaxWrite: 3, WriteFailAt: 2})
		p = NewPacketIO(fault, 64)
		if ob {
			_ = p.EnableOB20(OB20Options{ConnectionID: 42})
		} else {
			_ = p.EnableCompression()
		}
		err = p.WriteMessage(context.Background(), []byte("command"))
		var transport *IOError
		if !errors.As(err, &transport) || transport.Bytes != 3 || !errors.Is(err, testutil.ErrInjected) {
			t.Fatal(err)
		}
		p.ResetSequence()
		if err = p.WriteMessage(context.Background(), nil); !errors.Is(err, ErrDesynchronized) || buf.out.Len() != 3 {
			t.Fatal("partial write replayed", err)
		}
	}
}

func TestOB20CommandIdentity(t *testing.T) {
	buf := newBufferConn(nil)
	p := NewPacketIO(buf, 64)
	_ = p.EnableOB20(OB20Options{ConnectionID: 42, Checksum: true})
	p.frames.requestID = 0xfffffe
	for _, id := range []uint32{0xffffff, 0, 1} {
		p.ResetSequence()
		buf.out.Reset()
		if err := p.WriteMessage(context.Background(), []byte{'q'}); err != nil {
			t.Fatal(err)
		}
		sent := buf.out.Bytes()
		if uint32(int3(sent[15:])) != id || sent[3] != 0 || sent[18] != 0 || sent[34] != 0 || binary.LittleEndian.Uint32(sent[23:])&2 == 0 {
			t.Fatal("command boundary", sent)
		}
		response := ob20Fixture([]byte{1, 0, 0, 1, 'a'}, 1, 2)
		put3(response[15:], id)
		binary.LittleEndian.PutUint16(response[29:], crc16Reference(response[:29]))
		buf.in = bytes.NewReader(response)
		if got, err := p.ReadMessage(context.Background()); err != nil || string(got) != "a" {
			t.Fatal(got, err)
		}
	}
}

func TestFramedResetRejectsUnreadMessages(t *testing.T) {
	for _, ob := range []bool{false, true} {
		data := []byte{1, 0, 0, 0, 'a', 1, 0, 0, 1, 'b'}
		frame := compressedFixture(t, data, 0, false)
		if ob {
			frame = ob20Fixture(data, 0, 2)
		}
		buf := newBufferConn(frame)
		p := NewPacketIO(buf, 64)
		if ob {
			_ = p.EnableOB20(OB20Options{ConnectionID: 42})
		} else {
			_ = p.EnableCompression()
		}
		p.ResetSequence()
		if _, err := p.ReadMessage(context.Background()); err != nil {
			t.Fatal(err)
		}
		p.ResetSequence()
		if err := p.WriteMessage(context.Background(), []byte{'q'}); !errors.Is(err, ErrDesynchronized) || buf.out.Len() != 0 {
			t.Fatal("unread response reused", err)
		}
	}
}
