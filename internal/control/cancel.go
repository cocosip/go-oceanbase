// Package control owns session control commands and transaction lifecycle.
package control

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/connect"
	"github.com/cocosip/go-oceanbase/internal/session"
)

// CancelQuery sends one KILL QUERY through a private connection to the original entry.
// Target validation and pending-attempt synchronization never acquire the target operation.
func CancelQuery(ctx context.Context, configured *connect.Connector, target session.Target) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	id, ok := target.Info()
	if !ok {
		return fmt.Errorf("%w: invalid cancel target", dberrors.ErrResource)
	}
	if configured == nil {
		return fmt.Errorf("%w: CancelQuery requires this driver's Connector", dberrors.ErrUnsupported)
	}
	if configured.Closed() {
		return dberrors.ErrConnectorClosed
	}
	cfg := configured.Config()
	if cfg.Network != id.Network || cfg.Mode != config.ModeAuto && cfg.Mode != id.Mode {
		return fmt.Errorf("%w: cancel connector network or mode differs", dberrors.ErrUnsupported)
	}
	if id.TLSVerified && (cfg.TLSConfig == nil || cfg.TLSConfig.InsecureSkipVerify) {
		return fmt.Errorf("%w: cancel connector requires verified TLS", dberrors.ErrUnsupported)
	}
	ctx, stop := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer stop()
	pending, err := target.Begin(stop)
	if err != nil {
		return err
	}
	uncertain := false
	defer func() { pending.Finish(uncertain) }()
	cfg.Mode = id.Mode
	cfg.CreateDatabaseIfMissing = false
	cfg.Logger, cfg.Diagnostics = nil, config.DiagnosticOptions{}
	aux, err := connect.DialCandidate(ctx, cfg, id.Addr)
	if err != nil {
		return err
	}
	defer closeAuxiliary(aux)
	op, err := aux.Acquire(ctx)
	if err != nil {
		return err
	}
	defer op.Release()
	p := op.Protocol()
	if p.User != id.User || p.ProxyUser != id.ProxyUser || p.Mode != id.Mode || id.TLSVerified && !p.TLSVerified || p.Hello.ConnectionID == id.ConnectionID {
		return fmt.Errorf("%w: auxiliary connection identity differs or aliases target", dberrors.ErrResource)
	}
	if err = pending.BeforeSend(ctx, configured.Closed()); err != nil {
		return err
	}
	_, err = command.Start(ctx, op, []byte("\x03KILL QUERY "+strconv.FormatUint(uint64(id.ConnectionID), 10)), false)
	if err != nil {
		uncertain = !command.PreSend(err)
		op.Discard()
		return err
	}
	_, err = command.ReadOK(ctx, op)
	var server *dberrors.Error
	uncertain = err != nil && !errors.As(err, &server)
	return err
}

func closeAuxiliary(aux *session.Session) {
	ctx, stop := context.WithTimeout(context.Background(), aux.Config().Limits.CleanupTimeout)
	defer stop()
	if op, err := aux.Acquire(ctx); err == nil {
		_, _ = command.Start(ctx, op, []byte{1}, false)
		op.EndResponse()
		op.Release()
	}
	aux.Discard()
}
