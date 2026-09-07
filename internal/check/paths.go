package check

import "path/filepath"

// NewPaths builds a Finding's Paths from paths in the host's own form.
//
// IT EXISTS BECAUSE THE FIELD'S CONTRACT HAD NOTHING ENFORCING IT. Paths
// is documented as slash-separated on every platform, and a producer
// built on the standard library's relative-path helper hands back the
// HOST's separator — backslashes on Windows — which the struct accepts
// in silence. The result is a machine-readable field whose shape depends
// on the machine that produced it, in the one place a value is meant to
// be compared.
//
// The conversion is the standard library's, not a replacement of one
// byte with another, and the difference is the whole reason to use it.
// On Unix a backslash is an ordinary character in a filename: a file
// really can be called `weird\name.astro`, and a blind replacement would
// rename it in the report and point its owner at a directory that does
// not exist. filepath.ToSlash converts separators on the platform where
// backslash IS one and does nothing where it is not.
//
// No paths means nil rather than an empty slice: a finding about the
// project as a whole carries none, and the two would render as a
// difference nobody meant.
//
// It deliberately does no more than this. Making a path project-relative
// needs the project root, which this package does not have and should
// not learn.
func NewPaths(paths ...string) []string {
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, filepath.ToSlash(p))
	}
	return out
}
