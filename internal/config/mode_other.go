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
//
// holdsToken is what the file was found to contain, and it exists so the
// warning does not assert something the caller already knows is false. A
// file with nothing in it is still worth a word about its permissions —
// it will hold a token after the next login — but saying it "holds an
// access token" while this package is one line from reporting that it
// does not is the kind of small untruth that teaches people to stop
// reading warnings. When the answer could not be determined at all, the
// caller passes true: mentioning a token that is not there costs a
// sentence, and omitting one that is costs the point of the warning.
//
// THE PATH IS QUOTED, and it is the difference between advice and
// pasteable advice. A config directory with a space in it is entirely
// ordinary, and unquoted the command reads as two arguments and fails on
// the path the user was told to fix. Go's quoting is not a shell's, but
// inside double quotes the two agree on the cases that actually turn up
// here — a space, a tab, a quotation mark.
func modeWarning(path string, fi os.FileInfo, holdsToken bool) (string, bool) {
	perm := fi.Mode().Perm()
	if perm&0o077 == 0 {
		return "", true
	}
	holds := ""
	if holdsToken {
		holds = " and it holds an access token"
	}
	return fmt.Sprintf(
		"the config file at %s is readable by other users on this machine (its "+
			"permissions are %04o)%s. Fix it with: chmod 600 %q",
		path, perm, holds, path), true
}

// chmodFile pins the file to mode, whatever the process umask happens to
// be.
//
// THE REASON IS THAT A UMASK REMOVED BITS, and this is the call that
// puts them back. The mode passed when a file is CREATED is masked by
// the umask, which makes it a ceiling rather than a setting: under
// umask 0277 a file asked for at 0600 arrives at 0400, and this package
// cannot then read back what it just wrote. The other reason once given
// for this line — that the token never exists on disk in a file anyone
// else could read — is true and does not need it, because the temp file
// is created at 0600 already. Two comments in one package gave two
// different reasons for one line, and only one of them was load-bearing.
func chmodFile(f *os.File, mode os.FileMode) error {
	return f.Chmod(mode)
}
