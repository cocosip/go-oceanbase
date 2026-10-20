package control

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/authentication"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/connect"
	"github.com/cocosip/go-oceanbase/internal/cursor"
	"github.com/cocosip/go-oceanbase/internal/lob"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// ChangeUser reauthenticates and initializes an identity under the caller's connect deadline.
func (m *Manager) ChangeUser(ctx context.Context, op *session.Operation, cache *statement.Cache, resolver *catalog.Resolver, cursors *cursor.Manager, lobs *lob.Manager, credentials config.Credentials, database string, xaUsed bool) (err error) {
	if xaUsed || m.activeTx != nil || op.Protocol().OK.Status&1 != 0 || op.Protocol().OK.Status&2 == 0 {
		return fmt.Errorf("%w: ChangeUser requires autocommit without a transaction", dberrors.ErrUnsupported)
	}
	cfg, identity := op.Config(), op.Protocol()
	cfg.User, cfg.Password, cfg.Credentials = credentials.User, credentials.Password, nil
	cfg.ProxyUser = credentials.ProxyUser
	cfg.Database, cfg.Mode = database, identity.Mode
	cfg.CreateDatabaseIfMissing = false
	credentials, err = connect.ResolveCredentials(ctx, cfg)
	if err != nil {
		return err
	}
	encode := func(s string) ([]byte, error) {
		return command.EncodeSQL(s, cfg.Session.Charset, cfg.Limits.MaxMessageBytes-1)
	}
	user, err := encode(credentials.User)
	if err != nil {
		return err
	}
	if database == "" && identity.Mode == config.ModeOracle {
		database, _, _ = strings.Cut(credentials.User, "@")
	}
	db, err := encode(database)
	if err != nil {
		return err
	}
	attributes, err := connect.AuthenticationAttributes(cfg, credentials, identity.Capabilities)
	if err != nil {
		return err
	}
	name, nonce := identity.AuthPlugin, identity.AuthChallenge
	plugin, err := authentication.New(cfg.Auth, name)
	if err != nil {
		return err
	}
	use := &authentication.Use{Plugin: plugin}
	attempted := false
	defer func() {
		err = errors.Join(err, use.Close())
		if attempted && err != nil {
			op.Discard()
		}
	}()
	challenge := config.AuthChallenge{Nonce: slices.Clone(nonce), TLSVerified: identity.TLSVerified}
	response, err := authentication.Response(ctx, plugin, name, credentials, challenge)
	if err != nil {
		return err
	}
	payload, err := wire.EncodeChangeUser(wire.HandshakeResponse{Capabilities: identity.Capabilities, User: string(user), Auth: response, Database: string(db), Plugin: name, Attributes: attributes})
	if err != nil {
		return err
	}
	if int64(len(payload)) > cfg.Limits.MaxMessageBytes {
		return wire.ErrMessageTooLarge
	}
	op.EndCancellation(true)
	if err = invalidateResources(ctx, op, cursors, lobs, true); err != nil {
		return err
	}
	if err = cache.Clear(ctx, op); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	resolver.Clear()
	op.MarkDirty()
	op.AdvanceIdentity()
	m.ResetWarnings()
	attempted = true
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { op.Discard(); close(done) })
	defer func() {
		if !stop() {
			<-done
		}
		if cause := ctx.Err(); cause != nil {
			err = errors.Join(err, cause)
		}
	}()
	op.Packets().ResetSequence()
	if err = op.Packets().WriteMessage(ctx, payload); err != nil {
		return err
	}
	if err = connect.FinishAuthentication(ctx, cfg, &identity, credentials, name, use, challenge); err != nil {
		return err
	}
	if identity.OK.Status&8 != 0 {
		return wire.ErrMalformed
	}
	if err = use.Close(); err != nil {
		return err
	}
	identity.User, identity.ProxyUser = credentials.User, credentials.ProxyUser
	// The explicit call owns credentials; never retain its password or old provider.
	cfg.Password = ""
	op.SetIdentity(identity)
	op.SetConfig(cfg)
	if err = connect.Initialize(ctx, op, cfg); err != nil {
		return err
	}
	m.ResetWarnings()
	return ctx.Err()
}
