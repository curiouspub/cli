//go:build windows

package config

import "os"

// defaultConfigDir returns the config ROOT this platform uses when
// neither environment variable in Path applies.
//
// Here the platform's own answer is the right one: %AppData% is where a
// Windows user expects an application's settings to live, and ~/.config
// means nothing on this platform — it would be a dotted directory in a
// profile folder, invisible to every tool a Windows user has for looking
// at their own machine.
func defaultConfigDir() (string, error) {
	return os.UserConfigDir()
}
