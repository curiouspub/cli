package main

import (
	"fmt"
	"strings"

	"github.com/curiouspub/cli/internal/leakcheck"
	"github.com/curiouspub/cli/internal/rulefile"
)

// The two rule files this check reads. They belong to the content rule
// that scans file contents, and they are READ rather than copied — the
// proof that a rule file is load bearing is that deleting a line from it
// measurably changes what a check can see, and a checker consulting its
// own list would pass that proof while failing to do it.
const (
	citationPatternsPath = "scripts/citation-patterns.txt"
	vendorTermsPath      = "scripts/vendor-terms.txt"
)

// end is one side of the range as a source of rule files: the base
// revision, or the working tree at head. present distinguishes a file
// that is not there from one that could not be read, because the second
// is not an answer and must never be reported as the first.
type end func(path string) (text string, present bool, err error)

// Narrowing is a rule that the base declared and head does not.
//
// It is REPORTED, with the line named, and it fails the check. The union
// below means a narrowing cannot weaken the run that introduces it, so
// this is not a security control — it is the mechanism that makes giving
// something up a deliberate, visible act. An intentional narrowing then
// lands on its own, with nothing else in the range, which is the only
// shape in which anybody can see what is being given up.
type Narrowing struct {
	Path string

	// Rule is the id the manifest gives the retired rule.
	//
	// THE ID AND NOT THE RULE ITSELF, for the reason a finding carries an
	// id: a retired VENDOR term printed here is the provider name written
	// into a run's log, by the one report guaranteed to be holding it.
	// The id says which line of a published manifest to go and read.
	Rule string
}

// LoadRules builds the vocabulary from BOTH ENDS of the range: the union
// of each rule file as the base declares it and as head declares it.
//
// READING HEAD ALONE IS A HOLE, and it is the one the content rule's own
// best property opens. That rule reads its manifest from the working tree
// at run time, deliberately, so that deleting a line measurably changes
// what it catches. A range checker inheriting that reads the patterns AS
// THEY EXIST AFTER the range it is checking — so one push can delete the
// line that would catch it and add the message, together, and the checker
// loads the weakened file and passes. Two ideas that are each right, and
// whose combination is not.
//
// The union closes it: the base's copy of a deleted line is still in
// force for the range that deletes it.
func LoadRules(base, head end) (Rules, []Narrowing, error) {
	patternRules, patternNarrowings, err := unionOf(base, head, citationPatternsPath, verbatim)
	if err != nil {
		return Rules{}, nil, err
	}
	vendorRules, vendorNarrowings, err := unionOf(base, head, vendorTermsPath, strings.ToLower)
	if err != nil {
		return Rules{}, nil, err
	}

	engine, err := leakcheck.New(patternRules, vendorRules)
	if err != nil {
		return Rules{}, nil, fmt.Errorf("the union of %s and %s across the range: %w",
			citationPatternsPath, vendorTermsPath, err)
	}
	rules := Rules{Rules: engine}

	// THE EMPTY-INPUT REFUSAL. A vocabulary that lost its contents passes
	// everything and says nothing, which is indistinguishable from a
	// clean range. The content rule refuses on an empty pattern file for
	// the same reason; this is that refusal, on a union.
	if rules.Empty() {
		return Rules{}, nil, fmt.Errorf("the union of %s and %s across the range declares "+
			"%d pattern(s) and %d term(s) — with either at zero this check would pass "+
			"everything silently",
			citationPatternsPath, vendorTermsPath, len(patternRules), len(vendorRules))
	}

	return rules, append(patternNarrowings, vendorNarrowings...), nil
}

// verbatim is the sameness test for a file whose lines mean exactly what
// they spell.
func verbatim(line string) string { return line }

// unionOf reads one rule file at both ends and returns every data line
// either of them declares, plus the lines only the base had.
//
// A file ABSENT at the base is not a narrowing: the base predates it, and
// there is nothing to have given up. A file absent at HEAD is a narrowing
// of every line it used to carry, which is what deleting it means.
//
// SAMENESS IS THE FILE'S OWN QUESTION, which is why it arrives as an
// argument. Whether two lines are the same rule depends on how the line
// is read, and the two rule files read theirs differently: a term is
// looked up after lowering, so two spellings of one term are one rule and
// changing the case of a letter gives nothing up; a pattern is compiled
// as written, so two spellings are two different patterns and swapping
// one for the other really does retire the first. Compared verbatim, a
// case-only edit to a term reports a narrowing that did not happen — and
// a narrowing is a finding, so an edit that changed nothing fails a run
// and teaches the reader that this report cries wolf.
func unionOf(base, head end, path string, sameAs func(string) string) ([]rulefile.Rule, []Narrowing, error) {
	baseText, baseHad, err := base(path)
	if err != nil {
		return nil, nil, fmt.Errorf("reading %s at the base of the range: %w", path, err)
	}
	headText, headHas, err := head(path)
	if err != nil {
		return nil, nil, fmt.Errorf("reading %s at head: %w", path, err)
	}
	if !baseHad && !headHas {
		return nil, nil, fmt.Errorf("neither end of the range declares %s, so there is no "+
			"vocabulary to check against", path)
	}

	// BOTH ENDS ARE READ AS WHATEVER THEY WERE, and neither is policed
	// here. The id column arrived at a point in time and both ends of a
	// range can sit before it: the base is any commit in the history, and
	// head is whatever revision is checked out, which on a machine
	// examining an old push is also history. Parsed strictly, such a
	// revision does not load at all, and this check would report that it
	// could not assemble a vocabulary — an UNDETERMINED run rather than a
	// clean one — on every range old enough.
	//
	// THAT THE TREE KEEPS THE FORMAT IS A DIFFERENT RULE WITH A DIFFERENT
	// HOME. internal/guard loads these same files strictly, on the
	// working tree, on every run of the suite: a data line with no id, a
	// malformed one, or two rules answering to one handle fails there,
	// loudly, in the change that writes it. This reader's job is to be
	// able to read the past; that reader's job is to keep the present
	// well formed, and only one of them is looking at something anybody
	// can still edit.
	headRules := rulefile.ParseHistorical(path+" at head", headText)
	baseRules := rulefile.ParseHistorical(path+" at the base of the range", baseText)

	inHead := map[string]bool{}
	for _, rule := range headRules {
		inHead[sameAs(rule.Text)] = true
	}

	// THE BASE'S IDS ARE NOT CONSULTED, and that is the same ruling the
	// sameAs argument carries: what a rule IS is its text, and the id is
	// the handle used to talk about it. Keying the union on ids instead
	// would report a narrowing every time somebody renamed a handle
	// without giving anything up — and would MISS the real one, where a
	// rule's text is replaced under an id that stayed put.
	union := append([]rulefile.Rule(nil), headRules...)
	var narrowings []Narrowing
	seen := map[string]bool{}
	for _, rule := range baseRules {
		key := sameAs(rule.Text)
		if inHead[key] || seen[key] {
			continue
		}
		seen[key] = true
		union = append(union, rule)
		narrowings = append(narrowings, Narrowing{Path: path, Rule: rule.ID})
	}
	return union, narrowings, nil
}
