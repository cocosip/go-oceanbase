// Package auth implements authentication algorithms without transport or driver policy.
package auth

import "errors"

// ErrInvalidChallenge indicates malformed server challenge data.
var ErrInvalidChallenge = errors.New("auth: invalid challenge")
