package execution

import (
	"context"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/streaminput"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"net"
	"strings"
	"testing"
	"time"
)

func readCharsetUpload(ctx context.Context, t *testing.T, p *wire.PacketIO) []byte {
	t.Helper()
	var data []byte
	for {
		b, err := p.ReadMessage(ctx)
		if err != nil {
			t.Error(err)
			return data
		}
		if len(b) == 0 {
			return data
		}
		data = append(data, b...)
	}
}
func TestLocalInfileFilenameCharset(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close(); _ = server.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan []byte, 1)
	go func() { done <- readCharsetUpload(ctx, t, wire.NewPacketIO(server, 1024)) }()
	cfg := config.Config{Session: config.SessionOptions{Charset: "gbk"}, Limits: config.Limits{MaxMessageBytes: 1024, MaxLOBBytes: 1024}}
	physical := session.New(cfg, &session.Negotiated{Capabilities: wire.ClientLocalFiles, Packets: wire.NewPacketIO(client, 1024)})
	op, err := physical.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer op.Release()
	r := &Rows{op: op, input: &streaminput.Infile{Name: "输入", Reader: strings.NewReader("data")}}
	err = r.uploadInfile(ctx, []byte{251, 0xca, 0xe4, 0xc8, 0xeb})
	if err != nil {
		_ = client.Close()
	}
	got := <-done
	if err != nil || string(got) != "data" {
		t.Fatal("session charset filename mismatch", got, err)
	}
}
