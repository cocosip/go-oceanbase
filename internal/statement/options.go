package statement

import (
	"bytes"
	"slices"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/configspec"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// SupportsVersion keeps the statement package gate compatible while sharing
// OceanBase version parsing with the rest of the driver.
func SupportsVersion(version string, major, minor, patch uint32) bool {
	return configspec.SupportsVersion(version, major, minor, patch)
}

// SupportsPrepareExecute identifies the first native prepare-execute protocol version.
func SupportsPrepareExecute(version string) bool { return SupportsVersion(version, 2, 2, 76) }

// UsePrepareExecute applies the configured Oracle native-command gate.
func UsePrepareExecute(op *session.Operation) bool {
	p := op.Protocol()
	return op.Config().Execution.PrepareExecute && p.Mode == config.ModeOracle && SupportsPrepareExecute(p.OBVersion)
}

// ExecuteOptions applies the SQL checksum compatibility gate.
func ExecuteOptions(op *session.Operation, query []byte, combined bool) wire.ExecuteOptions {
	o := wire.ExecuteOptions{}
	p := op.Protocol()
	if op.Config().Execution.StatementChecksum && p.Mode == config.ModeOracle && (combined || SupportsVersion(p.OBVersion, 4, 1, 2)) {
		o.UseChecksum, o.SQLChecksum = true, wire.SQLChecksum(query)
	}
	return o
}

// ParameterOptions prepares type reuse state; AcknowledgeTypes commits only successful execution.
func (s *Statement) ParameterOptions(op *session.Operation, query []byte, params []wire.Parameter, combined bool) wire.ExecuteOptions {
	o := ExecuteOptions(op, query, combined)
	if !op.Config().Execution.ReuseParameterTypes {
		return o
	}
	o.ReuseTypes = s.id != 0 && sameParameterTypes(params, s.lastTypes)
	s.pendingTypes = make([]wire.Parameter, len(params))
	for i, p := range params {
		s.pendingTypes[i] = wire.Parameter{Type: p.Type, TypeMetadata: slices.Clone(p.TypeMetadata)}
	}
	return o
}

// AcknowledgeTypes drops all acknowledged types after a failed command.
func (s *Statement) AcknowledgeTypes(err error) {
	s.lastTypes = nil
	if err == nil {
		s.lastTypes = s.pendingTypes
	}
	s.pendingTypes = nil
}

func sameParameterTypes(a, b []wire.Parameter) bool {
	if len(a) != len(b) {
		return false
	}
	for i, p := range a {
		if p.Type != b[i].Type || !bytes.Equal(p.TypeMetadata, b[i].TypeMetadata) {
			return false
		}
	}
	return true
}
