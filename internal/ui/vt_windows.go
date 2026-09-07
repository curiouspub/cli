package ui

import (
	"io"

	"golang.org/x/sys/windows"
)

// terminalUnderstandsEscapes reports whether escape sequences written to
// w will be interpreted rather than printed, by ASKING THE CONSOLE and
// not by assuming.
//
// This function exists because assuming was wrong. A Windows console
// prints "\x1b[1m" literally unless ENABLE_VIRTUAL_TERMINAL_PROCESSING
// is set on the handle being written to, and nothing sets it here:
// x/term touches the input flag only, and only inside MakeRaw, which
// this package never calls. Windows Terminal enables it for itself;
// cmd.exe and PowerShell under classic conhost — every Windows 10 box,
// and the Windows 11 machines still defaulting to it — do not. A person
// there would have read a headline of "←[1mNo lockfile found.←[0m".
//
// CI did not and could not catch this: its standard input is not a
// console, so the colour path is never reached on the matrix at all. It
// is the invisible-on-the-author's-machine class exactly.
//
// The mode is SET rather than merely read, because setting it is how the
// question is answered honestly. A console that accepts the flag will
// interpret what follows; one that refuses reports so, and this returns
// false rather than writing escapes into it and hoping. The flag is left
// on afterwards deliberately: it is a property of the console this
// process is writing to for as long as it runs, and restoring it on exit
// would mean racing the terminator on every path out.
func terminalUnderstandsEscapes(w io.Writer) bool {
	fd, ok := terminalFd(w)
	if !ok {
		return false
	}
	handle := windows.Handle(fd)

	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		return false
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return true
	}
	return windows.SetConsoleMode(handle, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
