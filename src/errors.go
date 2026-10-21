package gogl

import (
	"errors"
	"fmt"

	"github.com/gherlein/gogl/src/auth"
	"github.com/gherlein/gogl/src/types"
)

// Sentinels are declared in the leaf packages that return them and re-exported
// here, so a consumer of the root package can test for any failure without
// importing types or auth. They are the same values, so errors.Is works across
// either path.
var (
	// ErrUnauthorized means the router rejected the credentials.
	ErrUnauthorized = auth.ErrUnauthorized

	// ErrNonceExpired means the login nonce died before it was used. Retriable.
	ErrNonceExpired = auth.ErrNonceExpired

	// ErrLoginRateLimited means the router's brute-force protection has locked the
	// account. Retrying does not help and makes the lockout longer.
	ErrLoginRateLimited = auth.ErrLoginRateLimited

	// ErrUnsupportedAlgorithm means the challenge named a crypt algorithm this
	// package does not implement. Never falls back to a weaker one.
	ErrUnsupportedAlgorithm = auth.ErrUnsupportedAlgorithm

	// ErrUnsupportedHashMethod means the challenge named a login digest this
	// package does not implement.
	ErrUnsupportedHashMethod = auth.ErrUnsupportedHashMethod

	// ErrNotFound means no matching record exists on the device.
	ErrNotFound = types.ErrNotFound

	// ErrConflict means a write would collide with an existing record.
	ErrConflict = types.ErrConflict

	// ErrDomainNotSet means the DNS domain has not been configured on the router.
	ErrDomainNotSet = types.ErrDomainNotSet

	// ErrReservationsExist means a network change was refused because reservations
	// are still present.
	ErrReservationsExist = types.ErrReservationsExist

	// ErrUnwritableContent means host-file content contains a character the
	// firmware refuses.
	ErrUnwritableContent = types.ErrUnwritableContent

	// ErrInvalidInput means a caller-supplied value is unfit to write.
	ErrInvalidInput = types.ErrInvalidInput

	// ErrWirelessSession means a wireless write was refused because the session
	// issuing it arrives over WiFi.
	ErrWirelessSession = types.ErrWirelessSession

	// ErrInvalidName means a reservation name failed validation. Returned rather
	// than escaped, because a bad name can corrupt dnsmasq's config.
	ErrInvalidName = types.ErrInvalidName

	// ErrOutsideSubnet means an address does not fall inside the router's LAN.
	ErrOutsideSubnet = types.ErrOutsideSubnet

	// ErrInvalidMAC means a MAC address was not parseable.
	ErrInvalidMAC = types.ErrInvalidMAC

	// ErrInvalidIP means an address was not a valid IPv4 address.
	ErrInvalidIP = types.ErrInvalidIP

	// ErrSessionExpired means the sid was stale. Normally handled internally by
	// one transparent re-login; surfaced only when that re-login also fails.
	ErrSessionExpired = errors.New("gogl: session expired")
)

// JSON-RPC error codes observed from GL.iNet firmware 4.x.
//
// PROVISIONAL: populated from placeholder values pending capture from live
// hardware. See docs/plan.md Phase 0.
const (
	CodeAccessDenied = -32000
	CodeNotFound     = -32001
)

// RPCError is a JSON-RPC error returned by the router. It carries the group and
// method that produced it so a failure is traceable to a call site.
type RPCError struct {
	Code    int
	Message string
	Group   string
	Method  string
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("gogl: rpc error %d on %s.%s: %s", e.Code, e.Group, e.Method, e.Message)
}

// Unwrap maps the router's numeric codes onto package sentinels so callers can
// use errors.Is without knowing the wire codes.
func (e *RPCError) Unwrap() error {
	switch e.Code {
	case CodeAccessDenied:
		return ErrSessionExpired
	case CodeNotFound:
		return ErrNotFound
	default:
		return nil
	}
}
