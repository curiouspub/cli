//go:build windows

package api

import (
	"errors"
	"syscall"
)

// wsaeconnrefused is Winsock's real "connection refused" error number
// (WSAECONNREFUSED). It is used directly here, rather than through
// syscall.ECONNREFUSED, because that constant is not this value on this
// platform: Go's own zerrors_windows.go declares syscall.ECONNREFUSED as
// APPLICATION_ERROR plus an offset, an invented value in Go's own
// reserved range rather than a real Winsock errno, and syscall.Errno's
// own Is method (syscall_windows.go) only special-cases permission,
// exist, not-exist and unsupported — it never maps this platform's real
// WSAECONNREFUSED onto that invented constant. errors.Is(err,
// syscall.ECONNREFUSED) is therefore always false on Windows, whatever
// actually went wrong, and this file exists to fix that by comparing
// against the real value instead.
const wsaeconnrefused = syscall.Errno(10061)

// isConnectionRefused reports whether err is Windows's real "connection
// refused" errno — see wsaeconnrefused's own comment for why
// syscall.ECONNREFUSED cannot be used for this check on this platform.
func isConnectionRefused(err error) bool {
	return errors.Is(err, wsaeconnrefused)
}
