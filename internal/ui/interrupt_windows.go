package ui

// terminateInterrupted ends a run that was interrupted.
//
// Windows has no WIFSIGNALED and no equivalent of re-raising to die of
// the signal: a process terminated by a console control event reports
// 0xC000013A (STATUS_CONTROL_C_EXIT), which is not what scripts written
// for this tool read. Exiting with 130 is the portable value, and it is
// what the Unix path also ends up reporting — reached differently there,
// because a Unix shell asks a different question.
//
// The two files exist because the answer genuinely differs by platform,
// not because one of them is a fallback for the other.
func terminateInterrupted(u *UI) {
	u.exit(interruptExitCode)
}
