//go:build unix

package flow

import "syscall"

// Reading a socket buffer back, on the platforms whose descriptor is an
// int.
//
// THE SPLIT IS THE FILE DESCRIPTOR'S TYPE AND NOTHING ELSE. Every one of
// the three legs this project gates on offers getsockopt through the
// standard library, with the same option names and the same meaning; the
// signature differs because a descriptor is an int here and a handle on
// Windows. So the pair of files is two conversions rather than two
// implementations, and nothing about the measurement lives in either.
//
// A BUILD-TAGGED FILE IS DARK UNTIL SOMETHING COMPILES IT, which is why
// this pair is written as a pair with no third fallback. A platform that
// is neither of these fails to build the flow package's tests, loudly,
// on the machine it is being built for — where a fallback returning "not
// supported" would let a leg go quietly unpinned and record a gap over
// an autotuned buffer as though it were a pinned one. The gate compiles
// both halves because it runs all three legs.

const (
	soSendBuffer    = syscall.SO_SNDBUF
	soReceiveBuffer = syscall.SO_RCVBUF
)

// socketBuffer asks the kernel what a socket's buffer actually is.
func socketBuffer(fd uintptr, option int) (int, error) {
	return syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, option)
}
