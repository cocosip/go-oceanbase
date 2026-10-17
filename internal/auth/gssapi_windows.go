//go:build windows

package auth

import (
	"errors"
	"math"
	"syscall"

	"github.com/alexbrainman/sspi"
)

type sspiContext struct {
	credentials *sspi.Credentials
	context     *sspi.Context
	target      *uint16
	maxToken    int
	started     bool
	complete    bool
}

// NewGSSContext uses the process's current Windows identity, never a DB password.
func NewGSSContext(spn, mechanism string) (GSSContext, error) {
	if !ValidGSSPrincipal(spn) {
		return nil, ErrInvalidChallenge
	}
	flags := uint32(sspi.ISC_REQ_CONNECTION)
	switch mechanism {
	case "Kerberos":
		flags |= sspi.ISC_REQ_MUTUAL_AUTH
	case "NTLM", "Negotiate":
	default:
		return nil, ErrGSSMechanism
	}
	info, err := sspi.QueryPackageInfo(mechanism)
	if err != nil {
		return nil, err
	}
	if info.MaxToken == 0 || info.MaxToken > 16<<20 {
		return nil, ErrInvalidChallenge
	}
	target, err := syscall.UTF16PtrFromString(spn)
	if err != nil {
		return nil, err
	}
	credentials, err := sspi.AcquireCredentials("", mechanism, sspi.SECPKG_CRED_OUTBOUND, nil)
	if err != nil {
		return nil, err
	}
	return &sspiContext{credentials: credentials, context: sspi.NewClientContext(credentials, flags), target: target, maxToken: int(info.MaxToken)}, nil
}

func (c *sspiContext) Step(token []byte) ([]byte, bool, error) {
	if c.context == nil || c.complete || (c.started && len(token) == 0) || uint64(len(token)) > math.MaxUint32 {
		return nil, false, ErrInvalidChallenge
	}
	var input sspi.SecBuffer
	input.Set(sspi.SECBUFFER_TOKEN, token)
	var in *sspi.SecBufferDesc
	if c.started {
		in = sspi.NewSecBufferDesc([]sspi.SecBuffer{input})
	}
	c.started = true
	buffer := make([]byte, c.maxToken)
	output := []sspi.SecBuffer{{}}
	output[0].Set(sspi.SECBUFFER_TOKEN, buffer)
	out := sspi.NewSecBufferDesc(output)
	status := c.context.Update(c.target, out, in)
	complete := status == sspi.SEC_E_OK || status == sspi.SEC_I_COMPLETE_NEEDED
	switch status {
	case sspi.SEC_I_COMPLETE_NEEDED, sspi.SEC_I_COMPLETE_AND_CONTINUE:
		if status = sspi.CompleteAuthToken(c.context.Handle, out); status != sspi.SEC_E_OK {
			return nil, false, status
		}
	case sspi.SEC_E_OK, sspi.SEC_I_CONTINUE_NEEDED:
	default:
		return nil, false, status
	}
	if output[0].BufferSize > uint32(len(buffer)) || (output[0].BufferSize == 0 && !complete) {
		return nil, false, ErrInvalidChallenge
	}
	if complete && c.context.RequestedFlags&sspi.ISC_REQ_MUTUAL_AUTH != 0 && c.context.EstablishedFlags&sspi.ISC_REQ_MUTUAL_AUTH == 0 {
		return nil, false, errors.New("auth: SSPI did not establish mutual authentication")
	}
	c.complete = complete
	if output[0].BufferSize == 0 {
		return nil, complete, nil
	}
	return buffer[:int(output[0].BufferSize)], complete, nil
}

func (c *sspiContext) Close() error {
	var err error
	if c.context != nil && c.context.Handle != nil {
		err = c.context.Release()
	}
	c.context = nil
	if c.credentials != nil {
		err = errors.Join(err, c.credentials.Release())
	}
	c.credentials = nil
	return err
}
