package authentication

import (
	"context"
	"errors"
	"fmt"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/auth"
)

type gssAuth struct {
	principal string
	native    auth.GSSContext
	complete  bool
}

func (p *gssAuth) Security(c config.AuthChallenge) config.AuthSecurity {
	_, mechanism, err := auth.GSSAPISeed(c.Nonce, p.principal)
	if err == nil && mechanism != "Kerberos" {
		return config.AuthRequireTLS
	}
	return config.AuthChallengeResponse
}

func (p *gssAuth) Respond(ctx context.Context, _ config.Credentials, c config.AuthChallenge) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var token []byte
	if p.native == nil {
		if c.More {
			return nil, auth.ErrInvalidChallenge
		}
		spn, mechanism, err := auth.GSSAPISeed(c.Nonce, p.principal)
		if err != nil {
			if errors.Is(err, auth.ErrGSSMechanism) {
				return nil, fmt.Errorf("%w: %w", dberrors.ErrUnsupported, err)
			}
			return nil, err
		}
		p.native, err = auth.NewGSSContext(spn, mechanism)
		if err != nil {
			if errors.Is(err, auth.ErrGSSUnavailable) || errors.Is(err, auth.ErrGSSMechanism) {
				return nil, fmt.Errorf("%w: %w", dberrors.ErrUnsupported, err)
			}
			return nil, err
		}
	} else {
		if !c.More || p.complete {
			return nil, auth.ErrInvalidChallenge
		}
		token = c.Data
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	response, complete, err := p.native.Step(token)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	p.complete = complete
	return response, nil
}

func (p *gssAuth) Complete() bool { return p.complete }

func (p *gssAuth) Close() error {
	if p.native == nil {
		return nil
	}
	err := p.native.Close()
	p.native = nil
	return err
}
