//go:build !windows

package config

import (
	"fmt"
	"os"
)

// modeWarning reports whether the config file is readable by anyone
// other than its owner, and returns checked=true because on this
// platform the mode bits mean exactly that.
//
// It WARNS rather than refusing. A user whose file was written by a
// buggy earlier build, or copied out of a backup, would be stranded by a
// refusal with no way to get past it; a loud message naming the file and
// the one command that fixes it gets the file fixed instead. The token
// is still returned, because it is still their token.
func modeWarning(path string, fi os.FileInfo) (string, bool) {
	perm := fi.Mode().Perm()
	if perm&0o077 == 0 {
		return "", true
	}
	return fmt.Sprintf(
		"the config file at %s is readable by other users on this machine (its "+
			"permissions are %04o) and it holds an access token. Fix it with: "+
			"chmod 600 %s", path, perm, path), true
}

// chmodFile pins the file to owner-read-and-write, whatever the process
// umask happens to be. The mode passed when a file is CREATED is masked
// by the umask, which makes it a ceiling rather than a setting — this is
// the call that makes the mode exact.
func chmodFile(f *os.File) error {
	return f.Chmod(0o600)
}
