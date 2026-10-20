// Package authentication owns plugin conversations and their cleanup lifetime.
package authentication

import (
	"context"
	"crypto/rsa"
	"fmt"
	"io"
	"reflect"
	"slices"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/auth"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// Names of the supported built-in authentication plugins.
const (
	Native      = "mysql_native_password"
	CachingSHA2 = "caching_sha2_password"
	SHA256      = "sha256_password"
	Clear       = "mysql_clear_password"
	Old         = "mysql_old_password"
	Ed25519     = "client_ed25519"
	PAM         = "dialog"
	GSSAPI      = "auth_gssapi_client"
)

// New creates an independent configured plugin for one authentication exchange.
func New(options config.AuthOptions, name string) (config.AuthPlugin, error) {
	if name == Old && !options.AllowOldPassword {
		return nil, fmt.Errorf("%w: legacy password authentication is disabled", dberrors.ErrUnsupported)
	}
	if factory, ok := options.Plugins[name]; ok {
		plugin := factory()
		if nilAuthPlugin(plugin) {
			return nil, fmt.Errorf("%w: nil authentication plugin", dberrors.ErrUnsupported)
		}
		return plugin, nil
	}
	switch name {
	case GSSAPI:
		return &gssAuth{principal: options.ServicePrincipalName}, nil
	case PAM:
		return &pamAuth{respond: options.PAM, charset: options.PasswordCharset}, nil
	case Native, CachingSHA2, SHA256, Clear, Old, Ed25519:
		return builtinAuth{name: name, key: options.RSAKey, charset: options.PasswordCharset}, nil
	default:
		return nil, fmt.Errorf("%w: authentication plugin", dberrors.ErrUnsupported)
	}
}

func nilAuthPlugin(plugin config.AuthPlugin) bool {
	if plugin == nil {
		return true
	}
	v := reflect.ValueOf(plugin)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func copyChallenge(c config.AuthChallenge) config.AuthChallenge {
	c.Nonce, c.Data = slices.Clone(c.Nonce), slices.Clone(c.Data)
	return c
}

// Response applies transport security before invoking the plugin with copied challenges.
func Response(ctx context.Context, plugin config.AuthPlugin, name string, credentials config.Credentials, c config.AuthChallenge) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	security := plugin.Security(copyChallenge(c))
	if security > config.AuthRequireTLS {
		return nil, fmt.Errorf("%w: authentication security level", dberrors.ErrUnsupported)
	}
	if (name == Clear || name == PAM || security == config.AuthRequireTLS) && !c.TLSVerified {
		return nil, dberrors.ErrTLSRequired
	}
	response, err := plugin.Respond(ctx, credentials, copyChallenge(c))
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return response, nil
}

// Use owns one mutable authentication plugin and closes it at most once.
type Use struct{ Plugin config.AuthPlugin }

// Close releases the current plugin once, including when it reports an error.
func (u *Use) Close() error {
	plugin := u.Plugin
	u.Plugin = nil
	if closer, ok := plugin.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

type builtinAuth struct {
	name    string
	key     *rsa.PublicKey
	charset string
}

func (p builtinAuth) Security(c config.AuthChallenge) config.AuthSecurity {
	if p.name == Clear || ((p.name == SHA256 || (p.name == CachingSHA2 && c.More)) && p.key == nil) {
		return config.AuthRequireTLS
	}
	return config.AuthChallengeResponse
}

func (p builtinAuth) Respond(_ context.Context, credentials config.Credentials, c config.AuthChallenge) ([]byte, error) {
	password, err := charset.Encode(p.charset, credentials.Password)
	if err != nil {
		return nil, err
	}
	credentials.Password = string(password)
	switch p.name {
	case Old, Ed25519:
		if c.More {
			return nil, wire.ErrMalformed
		}
		if p.name == Old {
			return auth.OldPassword(credentials.Password, c.Nonce)
		}
		return auth.Ed25519(credentials.Password, c.Nonce)
	case Native:
		if c.More {
			return nil, wire.ErrMalformed
		}
		return auth.Native(credentials.Password, c.Nonce)
	case CachingSHA2:
		if !c.More {
			return auth.CachingSHA2(credentials.Password, c.Nonce)
		}
		if len(c.Data) != 1 || c.Data[0] != 4 {
			return nil, wire.ErrMalformed
		}
	case SHA256:
		if c.More {
			return nil, wire.ErrMalformed
		}
	case Clear:
		if c.More {
			return nil, wire.ErrMalformed
		}
	}
	if c.TLSVerified {
		return auth.Clear(credentials.Password)
	}
	return auth.EncryptPassword(credentials.Password, c.Nonce, p.key)
}
