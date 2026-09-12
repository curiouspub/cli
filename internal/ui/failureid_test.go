package ui

import (
	"bytes"
	"strings"
	"testing"
)

// TestTheIdReachesTheTerminal.
//
// # An id nobody sees is an id nobody can quote
//
// The catalog's whole motivation is a token: something a person can put
// in a support message and a model can match on, which survives the
// headline being reworded. A FailureID field that satisfies every guard
// in this package and never reaches a stream satisfies the letter of the
// catalog and none of its purpose — and that is exactly the state this
// row was written against, because the suite stayed green when the
// rendering was added, and it would have stayed green if it had not been.
//
// REQUIRED MUTATION, run 2026-09-12: drop the id line from
// renderFailure. Reds here and nowhere else, which is the measurement:
// before this row existed, that mutation was invisible.
func TestTheIdReachesTheTerminal(t *testing.T) {
	var out bytes.Buffer
	u := New(nil, &out, &out)

	f := NewFailure(IDUploadLinkExpired,
		"The upload link ran out.",
		"Links are short-lived on purpose.", NextFreshDeploy,
		"Run it again — a fresh link is issued every time.")

	rendered := u.renderFailure(f)

	if !strings.Contains(rendered, "Failure ID: upload-link-expired") {
		t.Errorf("the id never reached the reader:\n%s", rendered)
	}
	// IT IS METADATA BESIDE THE MESSAGE, NEVER INSIDE IT. No authored
	// paragraph grows an identifier, so the copy a person reads is
	// exactly what its author wrote.
	for _, part := range []string{f.What, f.Why, f.NextText} {
		if strings.Contains(part, "upload-link-expired") {
			t.Errorf("the id leaked into authored copy: %q", part)
		}
	}
	// AND Error() IS UNCHANGED, because it is the headline and callers
	// compare against it.
	if f.Error() != f.What {
		t.Errorf("Error() = %q, want the headline %q", f.Error(), f.What)
	}
	// The id sits AFTER the copy, so a reader who does not want it stops
	// at the blank line above.
	if i, j := strings.Index(rendered, "fresh link"), strings.Index(rendered, "Failure ID:"); i > j {
		t.Error("the id line is printed before the copy it identifies")
	}
}

// TestAFailureWithNoIdRendersNoIdLine is the control. A row that only
// checked for the presence of the line would pass against a renderer
// that printed "Failure ID: " with nothing after it.
func TestAFailureWithNoIdRendersNoIdLine(t *testing.T) {
	var out bytes.Buffer
	u := New(nil, &out, &out)

	rendered := u.renderFailure(&Failure{What: "Something happened.",
		Why: "For a reason.", Next: NextGiveUp, NextText: "Report it."})

	if strings.Contains(rendered, "Failure ID:") {
		t.Errorf("a failure with no id printed an id label anyway:\n%s", rendered)
	}
}

// TestTheGeneralValidatorAcceptsAllFourActions, and the standing
// failures keep a stricter rule.
//
// # Two rules about one field, and they are not in conflict
//
// The general contract is MEMBERSHIP: a failure's action is one of the
// four declared values. That deliberately allows None, because a future
// family may genuinely have nothing to suggest.
//
// The standing failures — what a person meets when this program cannot
// ask them anything — are held to more. A refusal that suggests nothing
// is worst exactly there, because the reader has already run out of ways
// to interact: no terminal, no usable answer, a closed door. That rule
// lives next door in the published-failures row and rejects None.
//
// RULED 2026-09-12: the stricter rule stands as a family-level
// requirement, and general membership does not quietly replace it. This
// row exists so "allows None" is ASSERTED rather than merely unenforced
// — an allowance nobody tests is indistinguishable from an oversight,
// and the next person to read the pair would not know which it was.
func TestTheGeneralValidatorAcceptsAllFourActions(t *testing.T) {
	for _, action := range []NextAction{
		NextNone, NextFreshDeploy, NextWait, NextGiveUp,
	} {
		t.Run(string(action), func(t *testing.T) {
			f := NewFailure(IDInternalFault, "A thing happened.",
				"For a reason.", action, "Some words.")
			if f.Next != action {
				t.Errorf("the constructor changed the action: got %q, want %q",
					f.Next, action)
			}
			if !validAction(f.Next) {
				t.Errorf("%q is one of the four declared values and the validator "+
					"rejects it", action)
			}
		})
	}

	// AND THE ZERO VALUE IS NOT ONE OF THEM, which is the case the
	// previous rule admitted: `Next != NextNone` is true of "", because
	// NextNone is the string "None". The distinction is the reason the
	// fourth value exists at all.
	if validAction(NextAction("")) {
		t.Error("the validator accepts the zero value, so a failure nobody " +
			"filled in passes as one with nothing to suggest — which is the " +
			"distinction NextNone was added to make")
	}
	if validAction(NextAction("Retry")) {
		t.Error("the validator accepts a value outside the contract")
	}
}
