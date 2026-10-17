package oceanbase

import (
	"context"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestStatementParameterTypes(t *testing.T) {
	c := &conn{cfg: Config{Execution: ExecutionOptions{ReuseParameterTypes: true}}}
	s := &stmt{conn: c, id: 7}
	for i, p := range []wire.Parameter{
		{Type: 8}, {Type: 8}, {Type: 8 | 0x8000}, {Type: 6}, {Type: 6},
		{Type: 160, TypeMetadata: []byte{1}}, {Type: 160, TypeMetadata: []byte{2}}, {Type: 160, TypeMetadata: []byte{2}},
	} {
		o := s.parameterOptions(nil, []wire.Parameter{p}, false)
		want := i == 1 || i == 4 || i == 7
		if o.ReuseTypes != want || s.pendingTypes[0].Data != nil {
			t.Fatal(i, o)
		}
		s.lastTypes = s.pendingTypes
		if len(p.TypeMetadata) > 0 {
			p.TypeMetadata[0] = 99
		}
	}
}

func TestStatementTypeFailure(t *testing.T) {
	db := optimizationDB(t, ModeMySQL, ExecutionOptions{ReuseParameterTypes: true}, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, testBatchSQL, 7, 1, nil) {
			return
		}
		for i := int64(0); i < 3; i++ {
			if !readCommand(ctx, t, p, optimizationExecute(7, i, i == 1)) {
				return
			}
			response := commandPacket(1, 0, 2)
			if i == 1 {
				response = []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'x'}
			}
			if !sendPackets(ctx, t, p, response) {
				return
			}
		}
		if !readCommand(ctx, t, p, stmtPacket(25, 7)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	s, err := db.PrepareContext(context.Background(), testBatchSQL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for i := 0; i < 3; i++ {
		_, err = s.ExecContext(context.Background(), i)
		if (err != nil) != (i == 1) {
			t.Fatal(i, err)
		}
	}
}
