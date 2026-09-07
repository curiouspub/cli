//go:build !windows

package config

import (
	"os"
	"path/filepath"
)

// defaultConfigDir returns the config ROOT this platform uses when
// neither environment variable in Path applies. Everywhere but Windows
// that is ~/.config, and the choice is deliberate on macOS in
// particular.
//
// os.UserConfigDir would return ~/Library/Application Support there. It
// is the platform's own answer and it is the wrong one for this tool:
// developer CLIs on macOS keep their config in ~/.config, that is where
// the documentation tells people to look, and one path across both
// Unixes is one support answer instead of two. A user who wants the
// platform's location can say so with XDG_CONFIG_HOME.
func defaultConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config"), nil
}
