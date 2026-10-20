package authentication

import (
	"context"
	"fmt"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/auth"
	"github.com/cocosip/go-oceanbase/internal/charset"
)

type pamAuth struct {
	round   int
	respond func(context.Context, config.PAMChallenge) (string, error)
	charset string
}

func (*pamAuth) Security(config.AuthChallenge) config.AuthSecurity { return config.AuthRequireTLS }

func (p *pamAuth) Respond(ctx context.Context, credentials config.Credentials, challenge config.AuthChallenge) ([]byte, error) {
	data := challenge.Nonce
	if challenge.More {
		data = challenge.Data
	}
	prompt := config.PAMChallenge{Round: p.round + 1}
	var err error
	if len(data) != 0 {
		prompt.Prompt, prompt.Echo, prompt.Last, err = auth.PAMPrompt(data)
		if err != nil {
			return nil, err
		}
	}
	p.round++
	var password string
	if p.respond != nil {
		password, err = p.respond(ctx, prompt)
	} else if p.round == 1 {
		password = credentials.Password
	} else {
		return nil, fmt.Errorf("%w: additional PAM response requires Auth.PAM", dberrors.ErrUnsupported)
	}
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	encoded, err := charset.Encode(p.charset, password)
	if err != nil {
		return nil, err
	}
	return auth.Clear(string(encoded))
}
