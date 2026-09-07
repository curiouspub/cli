//go:build windows

package config

import "testing"

// TestLoadMakesNoPermissionClaimOnWindows pins the honest half of the
// permission check: this platform is not asked a question it cannot
// answer, and the answer it gives is "not checked" rather than silence.
//
// os.Stat reports a mode here that looks group- and world-readable on
// every file, because the mode bits are a translation rather than the
// real access control. Running the other platforms' check against that
// would warn on every load and mean nothing, and people stop reading a
// warning that always fires.
//
// The reason this is a test rather than a comment: "no warning" and
// "checked, and fine" are indistinguishable to a caller unless something
// carries the difference. PermissionsChecked carries it, and a future
// change that made this platform silently claim to have checked would
// red here.
//
// REQUIRED MUTATION: in modeWarning (mode_windows.go), return true as
// the second value. This test reds. (Recorded rather than run: this
// branch cannot execute on the developer's machine. It is type-checked
// for this platform on every push, and the matrix runs it.)
func TestLoadMakesNoPermissionClaimOnWindows(t *testing.T) {
	const endpoint = "https://api.example.com"
	path := hermeticPath(t)
	writeConfigFile(t, path, `{"version":1,"token":"`+testToken+
		`","api_url":"`+endpoint+`"}`)

	loaded, err := Load(endpoint)
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if loaded.PermissionsChecked {
		t.Error("PermissionsChecked = true on a platform where this build neither " +
			"sets nor reads the access control that actually decides who can read " +
			"the token file")
	}
	if len(loaded.Warnings) != 0 {
		t.Errorf("a warning was produced from a check this platform cannot make: %q",
			loaded.Warnings)
	}
	if string(loaded.Token) != testToken {
		t.Error("the token was withheld")
	}
}
