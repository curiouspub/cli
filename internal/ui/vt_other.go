//go:build !windows

package ui

import "io"

// terminalUnderstandsEscapes reports whether escape sequences written to
// w will be interpreted rather than printed.
//
// Everywhere but Windows the answer is yes for any terminal this program
// will meet: escape interpretation is the terminal's job and has been
// since before any of this existed. The two ways a Unix user says "not
// for me" — NO_COLOR and TERM=dumb — are already answered by the caller,
// and there is nothing left for this function to find out.
func terminalUnderstandsEscapes(io.Writer) bool { return true }
