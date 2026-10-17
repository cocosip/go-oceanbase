package oceanbase

import (
	"context"
	"fmt"

	"github.com/cocosip/go-oceanbase/internal/auth"
	"github.com/cocosip/go-oceanbase/internal/charset"
)

// PAMChallenge describes one UTF-8 dialog prompt. Round starts at one for each
// plugin use. Last is a server hint; authentication still requires a final OK.
type PAMChallenge struct {
	Round      int
	Prompt     string
	Echo, Last bool
}

type pamAuth struct {
	round   int
	respond func(context.Context, PAMChallenge) (string, error)
	charset string
}

func (*pamAuth) Security(AuthChallenge) AuthSecurity { return AuthRequireTLS }

func (p *pamAuth) Respond(ctx context.Context, credentials Credentials, challenge AuthChallenge) ([]byte, error) {
	data := challenge.Nonce
	if challenge.More {
		data = challenge.Data
	}
	prompt := PAMChallenge{Round: p.round + 1}
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
		return nil, fmt.Errorf("%w: additional PAM response requires Auth.PAM", ErrUnsupported)
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
