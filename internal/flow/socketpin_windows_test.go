//go:build windows

package flow

import "syscall"

// Reading a socket buffer back, on the platform whose descriptor is a
// handle. See the comment in the file beside this one: the pair is two
// conversions rather than two implementations, and it is deliberately a
// pair with no fallback.

const (
	soSendBuffer    = syscall.SO_SNDBUF
	soReceiveBuffer = syscall.SO_RCVBUF
)

// socketBuffer asks the kernel what a socket's buffer actually is.
func socketBuffer(fd uintptr, option int) (int, error) {
	return syscall.GetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, option)
}
