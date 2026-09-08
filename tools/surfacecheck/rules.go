package main

import (
	"fmt"
	"regexp"
	"strings"
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

// Narrowing is a rule line that the base declared and head does not.
//
// It is REPORTED, with the line named, and it fails the check. The union
// below means a narrowing cannot weaken the run that introduces it, so
// this is not a security control — it is the mechanism that makes giving
// something up a deliberate, visible act. An intentional narrowing then
// lands on its own, with nothing else in the range, which is the only
// shape in which anybody can see what is being given up.
type Narrowing struct {
	Path string
	Line string
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
	patternLines, patternNarrowings, err := unionOf(base, head, citationPatternsPath)
	if err != nil {
		return Rules{}, nil, err
	}
	vendorLines, vendorNarrowings, err := unionOf(base, head, vendorTermsPath)
	if err != nil {
		return Rules{}, nil, err
	}

	rules := Rules{vendor: map[string]bool{}}
	for _, line := range patternLines {
		re, err := regexp.Compile(line)
		if err != nil {
			return Rules{}, nil, fmt.Errorf("%s declares %q, which is not a pattern this "+
				"check can compile: %w", citationPatternsPath, line, err)
		}
		rules.patterns = append(rules.patterns, re)
	}
	for _, line := range vendorLines {
		rules.vendor[strings.ToLower(line)] = true
	}

	// THE EMPTY-INPUT REFUSAL. A vocabulary that lost its contents passes
	// everything and says nothing, which is indistinguishable from a
	// clean range. The content rule refuses on an empty pattern file for
	// the same reason; this is that refusal, on a union.
	if rules.Empty() {
		return Rules{}, nil, fmt.Errorf("the union of %s and %s across the range declares "+
			"%d pattern(s) and %d term(s) — with either at zero this check would pass "+
			"everything silently",
			citationPatternsPath, vendorTermsPath, len(rules.patterns), len(rules.vendor))
	}

	return rules, append(patternNarrowings, vendorNarrowings...), nil
}

// unionOf reads one rule file at both ends and returns every data line
// either of them declares, plus the lines only the base had.
//
// A file ABSENT at the base is not a narrowing: the base predates it, and
// there is nothing to have given up. A file absent at HEAD is a narrowing
// of every line it used to carry, which is what deleting it means.
func unionOf(base, head end, path string) ([]string, []Narrowing, error) {
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

	headLines := dataLines(headText)
	inHead := map[string]bool{}
	for _, line := range headLines {
		inHead[line] = true
	}

	union := append([]string(nil), headLines...)
	var narrowings []Narrowing
	seen := map[string]bool{}
	for _, line := range dataLines(baseText) {
		if inHead[line] || seen[line] {
			continue
		}
		seen[line] = true
		union = append(union, line)
		narrowings = append(narrowings, Narrowing{Path: path, Line: line})
	}
	return union, narrowings, nil
}

// dataLines returns the lines of a rule file that state a rule: blanks
// and comments carry prose for a reader and declare nothing.
func dataLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}
