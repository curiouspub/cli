//go:build unix

package config

import (
	"os"
	"strings"
	"testing"
)

// TestLoadUnreadableFileIsReportedNotFatal covers the file that is there
// and cannot be read — a mode somebody set by hand, a file restored from
// a backup under another account, a directory whose permissions changed
// underneath it. It is the one read failure that is not "no file", and
// nothing exercised it.
//
// It has to stay recoverable. A user who cannot read their own token
// still has to be able to run the command and log in again; ending the
// run would leave them with no way past a problem they can see.
//
// # Why the build tag AND a runtime probe, which is one more guard than usual
//
// The tag is because a mode of zero does not mean this anywhere else:
// on Windows the mode bits are a translation, and taking the read bit
// away toggles a read-only attribute that does not gate reading at all.
// So the row cannot express its own fixture there and is compiled out.
//
// The probe is because the tag is not sufficient EVEN on the platforms
// it selects. A process running as the superuser ignores permission
// bits — which a container commonly is — and some filesystems do not
// enforce them either. Both produce a fixture that is not unreadable,
// and a row that carried on would then assert nothing while looking
// green. So it attempts the read first and skips with the reason when
// the environment declines to produce the failure.
//
// STATED CAVEAT rather than an implied one: when this skips, nothing in
// this package covers the unreadable-file branch on that machine. The
// skip is declared in the manifest, so it is printed with its reason on
// every run rather than passing in silence.
//
// REQUIRED MUTATION: in Load (config.go), return the read error to the
// caller instead of reporting it through the Config. This row reds on
// the first assertion — the run ends where it should have continued into
// a login. Run, observed red, and the file restored from a
// checksum-verified copy.
func TestLoadUnreadableFileIsReportedNotFatal(t *testing.T) {
	const endpoint = "https://api.example.com"
	path := hermeticPath(t)
	writeConfigFile(t, path, `{"version":1,"token":"`+testToken+
		`","api_url":"`+endpoint+`"}`)
	before := readFile(t, path)

	if err := os.Chmod(path, 0); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	// Put the mode back before the harness tries to clean up, and
	// before the assertions below read the file again.
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	// THE CAPABILITY PROBE. Taking the bits away is a request, not a
	// result: the superuser ignores them, and so do some filesystems.
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("this process can still read a file with no permission bits set — " +
			"the superuser ignores them and some filesystems do not enforce them — " +
			"so the failure this row needs cannot be produced here")
	}

	loaded, err := Load(endpoint)
	if err != nil {
		t.Fatalf("Load() ended the run (%v) — a file the user cannot read is a "+
			"problem they can fix, and they have to be able to log in again in the "+
			"meantime", err)
	}
	if loaded.Token != "" {
		t.Errorf("a token came back out of a file that could not be read")
	}
	if loaded.NoTokenReason == nil {
		t.Fatalf("nothing was reported to the user")
	}
	msg := loaded.NoTokenReason.Error()
	if !strings.Contains(msg, path) {
		t.Errorf("the message does not name the file: %q", msg)
	}
	if !strings.Contains(msg, "permission") {
		t.Errorf("the message does not point at the thing that is actually wrong: %q", msg)
	}

	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("restoring the mode of %s: %v", path, err)
	}
	if after := readFile(t, path); string(after) != string(before) {
		t.Errorf("the load path modified a file it could not even read")
	}
}
