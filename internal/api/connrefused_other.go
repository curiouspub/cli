//go:build !windows

package api

import (
	"errors"
	"syscall"
)

// isConnectionRefused reports whether err is the platform's own
// "connection refused" errno. On every platform this build tag covers,
// syscall.ECONNREFUSED is the real value the kernel raises for a refused
// connection, so comparing against it with errors.Is is correct and
// sufficient. See connrefused_windows.go for the one platform where that
// stops being true.
func isConnectionRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
}
