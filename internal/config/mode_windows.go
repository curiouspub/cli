//go:build windows

package config

import "os"

// modeWarning makes NO claim on this platform, and says so by returning
// checked=false.
//
// Windows does not express "only the owner may read this" through the
// mode bits; that is an ACL, which this build does not set and does not
// read. os.Stat reports a mode here that looks group- and
// world-readable on every file, so the check the other platforms run
// would fire on every single load and mean nothing.
//
// The honest report is therefore "not checked" rather than either a
// warning nobody can act on or a silence that reads as approval. The
// token file inherits whatever permissions its directory carries, and
// the second return value is what lets a caller say so instead of
// implying a protection that is not there.
func modeWarning(string, os.FileInfo) (string, bool) {
	return "", false
}

// chmodFile does nothing on this platform, on purpose.
//
// os.File.Chmod here maps the mode onto a single read-only attribute, so
// asking for 0600 would at best be a no-op and at worst clear a
// read-only flag somebody set deliberately. It cannot express the thing
// the other platforms use it for, and a call that appears to restrict a
// file without restricting it is worse than no call at all.
func chmodFile(*os.File) error {
	return nil
}
