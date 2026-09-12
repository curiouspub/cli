package ui

import "strings"

// Closed is a run that ended because the door is shut to this caller
// right now, and it is NOT a Failure.
//
// # Why it is its own type
//
// Joining the waitlist is the clearest case. "You're on the list." is a
// success: the run did what the person asked, the server recorded them,
// and there is nothing wrong with their project or their machine. It was
// a *ui.Failure only because a Failure was the only shape this package
// had for "the run stops here and here is why", and that made it turn up
// in the failure catalog, in the troubleshooting section, and in every
// census of the ways a deploy can go wrong. It is none of those things.
//
// So the catalog carries no id for it, and the constructions guard
// asserts it is no longer among them. Its home in the README is a
// section of its own — when the door is closed — beside the trial's own
// story rather than among the faults.
//
// # It still costs exit 3, and that is deliberate
//
// A script has to be able to tell "your deploy did not happen" from
// "your deploy happened". The run did not deploy anything, so the status
// is non-zero; and the number is the scoped closed-door code rather than
// the general failure code, because nothing failed.
//
// # It carries no Detail, and cannot
//
// A Detail is somebody else's sentence. Everything here is this
// program's own copy about a state it chose to report, so there is no
// quotation to hold and no escaping decision to get wrong.
type Closed struct {
	What     string
	Why      string
	NextText string
}

// Error makes a Closed travel as an error, the same way a Failure does,
// so the sequence can return it without a second channel.
func (c *Closed) Error() string { return c.What }

// paragraphs is the copy in the order it is shown, empties dropped.
func (c *Closed) paragraphs() []string {
	parts := make([]string, 0, 3)
	for _, part := range []string{c.What, c.Why, c.NextText} {
		if part != "" {
			parts = append(parts, SanitizeLines(part))
		}
	}
	return parts
}

// renderClosed produces the exact bytes the closed-door ending writes.
//
// IT IS THE SAME SHAPE AS renderFailure ON PURPOSE. A reader should not
// be able to tell from the layout which type carried the words — the
// distinction is about what the estate counts, not about what a person
// sees — so the paragraphs, the styling of the headline and the trailing
// newline all match. The transcript rows that covered this ending before
// it moved types are what says so.
func (u *UI) renderClosed(c *Closed) string {
	if c == nil {
		return ""
	}
	rendered := c.paragraphs()
	if len(rendered) == 0 {
		return ""
	}
	if c.What != "" {
		rendered[0] = u.styled(rendered[0])
	}
	return strings.Join(rendered, "\n\n") + "\n"
}
