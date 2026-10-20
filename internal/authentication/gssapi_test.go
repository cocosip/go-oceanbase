package authentication

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
)

func TestAuthPluginGSSMechanismSecurity(t *testing.T) {
	for _, mechanism := range []string{"NTLM", "Negotiate"} {
		plugin, e := New(config.AuthOptions{}, GSSAPI)
		if e != nil {
			t.Fatal(e)
		}
		_, e = Response(context.Background(), plugin, GSSAPI, config.Credentials{User: "app"}, config.AuthChallenge{Nonce: []byte("service/localhost\x00" + mechanism + "\x00")})
		if !errors.Is(e, dberrors.ErrTLSRequired) {
			t.Fatal("non-Kerberos GSS token without verified TLS", e)
		}
	}
}

type gssStepProbe struct{ calls int }

func (p *gssStepProbe) Step([]byte) ([]byte, bool, error) {
	p.calls++
	return []byte("must-not-send"), false, nil
}

func (*gssStepProbe) Close() error { return nil }

func TestAuthPluginGSSCanceledBeforeNativeStep(t *testing.T) {
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &cancelAtCheckContext{Context: base, cancel: cancel}
	probe := &gssStepProbe{}
	plugin := &gssAuth{native: probe}
	out, err := plugin.Respond(ctx, config.Credentials{}, config.AuthChallenge{More: true, Data: []byte{0x60, 1}})
	if !errors.Is(err, context.Canceled) || out != nil || probe.calls != 0 {
		t.Fatal("native step started after cancellation", out, probe.calls, err)
	}
}

type cancelAtCheckContext struct {
	context.Context
	cancel context.CancelFunc
	once   sync.Once
}

func (c *cancelAtCheckContext) Err() error { err := c.Context.Err(); c.once.Do(c.cancel); return err }
