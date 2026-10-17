package wire

import (
	"bytes"
	"context"
	"testing"
)

func TestDiagnosticBounds(t *testing.T) {
	p := NewPacketIO(newBufferConn(nil), 64)
	p.EnableDiagnostics(2, 1)
	for range 4 {
		p.ResetSequence()
		if err := p.WriteMessage(context.Background(), []byte{3, 's', 'e', 'c', 'r', 'e', 't'}); err != nil {
			t.Fatal(err)
		}
		p.notePacket(false, 1, 5, []byte{0}, nil)
	}
	records, dropped := p.TakeDiagnostics()
	if len(records) != 2 || dropped != 2 {
		t.Fatal(records, dropped)
	}
	for _, r := range records {
		if r.WriteBytes != 11 || r.ReadBytes != 5 || len(r.Packets) != 1 || r.DroppedPackets != 1 || r.SQLBytes != 6 || len(r.SQLFingerprint) != 64 {
			t.Fatal(r)
		}
	}
	if records, dropped = p.TakeDiagnostics(); len(records) != 0 || dropped != 0 {
		t.Fatal(records, dropped)
	}
}

func TestDiagnosticAuthenticationExcluded(t *testing.T) {
	c := newBufferConn(nil)
	p := NewPacketIO(c, 64)
	if err := p.WriteMessage(context.Background(), []byte("credential-secret")); err != nil {
		t.Fatal(err)
	}
	p.ResetSequence()
	p.EnableDiagnostics(4, 4)
	if err := p.WriteMessage(context.Background(), []byte{14}); err != nil {
		t.Fatal(err)
	}
	c.in = bytes.NewReader([]byte{7, 0, 0, 1, 0})
	if _, err := p.ReadMessage(context.Background()); err == nil {
		t.Fatal("partial response accepted")
	}
	records, _ := p.TakeDiagnostics()
	if len(records) != 1 || !records[0].Failed || records[0].ReadBytes != 5 || records[0].WriteBytes != 5 || records[0].SQLFingerprint != "" {
		t.Fatal(records)
	}
}

func TestDiagnosticPipeline(t *testing.T) {
	c := newBufferConn([]byte{1, 0, 0, 1, 0, 1, 0, 0, 1, 0})
	p := NewPacketIO(c, 64)
	p.EnableDiagnostics(4, 4)
	_, err := p.Pipeline(context.Background(), [][]byte{{14}, {14}}, func(int) error { _, e := p.ReadMessage(context.Background()); return e })
	if err != nil {
		t.Fatal(err)
	}
	records, dropped := p.TakeDiagnostics()
	if len(records) != 2 || dropped != 0 {
		t.Fatal(records, dropped)
	}
	for _, r := range records {
		if r.Command != 14 || r.WriteBytes != 5 || r.ReadBytes != 5 || r.Failed || len(r.Packets) != 2 {
			t.Fatal(r)
		}
	}
}
