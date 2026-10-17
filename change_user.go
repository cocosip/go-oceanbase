package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

type changeUserRequest struct {
	credentials Credentials
	database    string
}

// ChangeUser reauthenticates a pinned connection and reruns Config.Session initialization.
// It requires autocommit without a transaction. Old statements and resource handles expire.
// An empty database selects no MySQL database or the new Oracle user's default schema.
// After sending the change, failure closes the session; success remains usable until
// pinned.Close discards it. The original Connector and its credentials are not changed.
func ChangeUser(ctx context.Context, pinned *sql.Conn, credentials Credentials, database string) error {
	if pinned == nil {
		return fmt.Errorf("oceanbase: a non-nil *sql.Conn is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := pinned.ExecContext(ctx, "", changeUserRequest{credentials: credentials, database: database})
	return err
}

func (c *conn) execChangeUser(ctx context.Context, request changeUserRequest) (driver.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.ConnectTimeout)
	defer cancel()
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.unlock()
	if c.closed.Load() {
		return nil, driver.ErrBadConn
	}
	if c.responseActive {
		return nil, errResponseActive
	}
	if c.xaUsed || c.activeTx != nil || c.session.ok.Status&1 != 0 || c.session.ok.Status&2 == 0 {
		return nil, fmt.Errorf("%w: ChangeUser requires autocommit without a transaction", ErrUnsupported)
	}
	return result{known: true}, c.changeUserLocked(ctx, request)
}

func (c *conn) changeUserLocked(ctx context.Context, request changeUserRequest) (err error) {
	cfg := c.cfg
	cfg.User, cfg.Password, cfg.Credentials = request.credentials.User, request.credentials.Password, nil
	cfg.Database, cfg.Mode = request.database, c.session.mode
	authenticator := &connector{cfg: cfg}
	credentials, err := authenticator.credentials(ctx)
	if err != nil {
		return err
	}
	user, err := c.encodeSQL(credentials.User)
	if err != nil {
		return err
	}
	database := request.database
	if database == "" && c.session.mode == ModeOracle {
		database, _, _ = strings.Cut(credentials.User, "@")
	}
	db, err := c.encodeSQL(database)
	if err != nil {
		return err
	}
	name, nonce := c.session.authPlugin, c.session.authChallenge
	plugin, err := authenticator.newAuthPlugin(name)
	if err != nil {
		return err
	}
	use := &authPluginUse{plugin: plugin}
	attempted := false
	defer func() {
		err = errors.Join(err, use.close())
		if attempted && err != nil {
			c.discard()
		}
	}()
	challenge := AuthChallenge{Nonce: slices.Clone(nonce), TLSVerified: c.session.tlsVerified}
	response, err := authResponse(ctx, plugin, name, credentials, challenge)
	if err != nil {
		return err
	}
	payload, err := wire.EncodeChangeUser(wire.HandshakeResponse{Capabilities: c.session.capabilities, User: string(user), Auth: response, Database: string(db), Plugin: name, Attributes: authenticator.protocolAttributes()})
	if err != nil {
		return err
	}
	if int64(len(payload)) > c.cfg.Limits.MaxMessageBytes {
		return wire.ErrMessageTooLarge
	}
	c.endCancelExecution(true)
	if err = c.invalidateResourcesLocked(ctx, true); err != nil {
		return err
	}
	if err = c.clearStatementsLocked(ctx); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	c.clearDescriptors()
	c.sessionDirty, c.statementEpoch = true, new(resourceEpoch)
	c.lastWarnings = WarningSummary{}
	c.warningsCleared = true
	attempted = true
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { c.discard(); close(done) })
	defer func() {
		if !stop() {
			<-done
		}
		if cause := ctx.Err(); cause != nil {
			err = errors.Join(err, cause)
		}
	}()
	c.session.packets.ResetSequence()
	if err = c.session.packets.WriteMessage(ctx, payload); err != nil {
		return err
	}
	if err = authenticator.finishAuthentication(ctx, c.session, credentials, name, use, challenge); err != nil {
		return err
	}
	if c.session.ok.Status&8 != 0 {
		return wire.ErrMalformed
	}
	if err = use.close(); err != nil {
		return err
	}
	c.session.user = credentials.User
	// The explicit call owns these credentials; never retain its password or invoke the old provider.
	cfg.Password = ""
	c.cfg = cfg
	if err = c.initializeLocked(ctx); err != nil {
		return err
	}
	c.lastWarnings = WarningSummary{}
	c.warningsCleared = true
	return ctx.Err()
}
