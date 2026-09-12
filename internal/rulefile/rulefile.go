// Package rulefile reads the manifests this repository states its own
// rules in: one rule per line, each carrying a stable id.
//
// WHY AN ID COLUMN EXISTS AT ALL. A rule has to be referable from
// somewhere that is not the file — a report, a ledger of known historical
// matches — and until now the only handle was the rule's own text. That
// fails in both directions. A citation pattern is a regular expression,
// so quoting it into a report means quoting it into a log; and the
// vocabulary of provider names had no handle at all, since every one of
// its findings was labelled with the same generic phrase and no two
// entries could be told apart.
//
// THE IDS ARE OPAQUE WHERE THE RULE'S TEXT IS THE THING BEING DEFENDED,
// and that is the whole reason they are not simply the rule spelled in
// lowercase. A report naming "the provider term that matched" publishes
// the provider name into a run's log, which on a fork's pull request is
// more public than the surface it was reading. So the manifests whose
// entries ARE the sensitive strings number their rules, and the manifest
// whose entries describe SHAPES names them. The id sits on the same line
// as the rule, so nothing about the file is harder to read.
//
// IDS ARE STABLE, AND STABILITY IS THE PROPERTY THEY ARE FOR. A recorded
// match is remembered by its id; renumbering a file therefore silently
// re-points every record that named one. There is no way to catch that
// from inside the file, so it is caught from outside: a ledger entry
// whose id no longer matches what the entry describes is a failure rather
// than a line that quietly stopped meaning anything.
package rulefile

import (
	"fmt"
	"regexp"
	"strings"
)

// Rule is one line of a manifest: the handle, the rule, and where it was
// written so a complaint can name the line somebody has to edit.
type Rule struct {
	ID   string
	Text string
	Line int
}

// idShape is what an id may be spelled with: lowercase alphanumerics in
// hyphen-separated groups. Narrow on purpose — an id is a handle that
// travels into ledgers and reports, so it may not contain whitespace (it
// is separated from the rule by whitespace), a "#" (it would look like a
// comment), or a capital letter (two ids differing only in case are two
// spellings of one handle and would be a very quiet way to lose a
// record).
var idShape = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Parse returns every rule a manifest declares.
//
// Blanks and comments carry prose for a reader and declare nothing, which
// is the handling every one of these files already had and which is kept
// exactly: a line starting with "#" is not a rule, and neither is an
// empty one.
//
// A DATA LINE IS AN ID, WHITESPACE, AND THEN THE RULE VERBATIM. The rule
// is whatever follows the first run of whitespace, so a rule containing
// spaces survives intact — several citation patterns match phrases and
// would be cut in half by a field split. The whole line is trimmed first,
// which is the handling these files already had: trailing whitespace on a
// regular expression is invisible and changes what it matches.
//
// name is the path the text came from, used only so that a complaint
// names a file a reader can open.
func Parse(name, text string) ([]Rule, error) {
	var out []Rule
	seen := map[string]int{}
	for i, raw := range strings.Split(text, "\n") {
		lineNumber := i + 1
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		id, rest, found := cut(line)
		if !found {
			return nil, fmt.Errorf("%s:%d: %q is an id with no rule after it. Every data "+
				"line here is an id, a space, and then the rule; a line carrying only one of "+
				"the two is a rule nothing can refer to or an id that forbids nothing",
				name, lineNumber, line)
		}
		if !idShape.MatchString(id) {
			return nil, fmt.Errorf("%s:%d: %q is not an id. An id is lowercase alphanumerics "+
				"in hyphen-separated groups, because it is a handle that travels into ledgers "+
				"and reports and has to survive being written down twice", name, lineNumber, id)
		}
		if first, ok := seen[id]; ok {
			return nil, fmt.Errorf("%s:%d: the id %q is already used on line %d. Two rules "+
				"answering to one handle make every record naming it ambiguous, and the record "+
				"is the thing the id exists for", name, lineNumber, id, first)
		}
		seen[id] = lineNumber
		out = append(out, Rule{ID: id, Text: rest, Line: lineNumber})
	}
	return out, nil
}

// cut splits a data line into its id and its rule at the first run of
// whitespace. It is written out rather than reached for through a field
// split because the rule half must come back VERBATIM — internal spaces
// and all — and a field split would hand back the first word of a phrase
// pattern and drop the rest.
func cut(line string) (id, rest string, found bool) {
	for i := 0; i < len(line); i++ {
		if line[i] == ' ' || line[i] == '\t' {
			rest = strings.TrimLeft(line[i:], " \t")
			if rest == "" {
				return "", "", false
			}
			return line[:i], rest, true
		}
	}
	return "", "", false
}

// ParseHistorical reads a rule file AS SOME EARLIER REVISION WROTE IT,
// and never fails.
//
// WHY IT EXISTS. The id column arrived at a point in time, and one reader
// of these files deliberately looks BEFORE that point: the range check
// reads each manifest at both ends of the range it is checking, so that a
// line deleted by the very push being checked is still in force for it.
// The base of a range can be any commit in this repository's history, and
// every commit before the column was added spells its rules as bare
// lines. Parsed strictly, those revisions do not load at all — and the
// check would report "the vocabulary could not be assembled", which is an
// undetermined run rather than a clean one, on every range whose base is
// old enough.
//
// THE FALLBACK IS TRIED SECOND AND IS ALL-OR-NOTHING, rather than
// per-line, because a per-line guess cannot be made safely: several
// citation patterns match PHRASES and contain spaces of their own, so
// "does this line have an id" has no local answer. Strict first, and only
// if the whole file refuses does every data line become one rule.
//
// THE SYNTHESISED ID SAYS WHAT IT IS. A rule the base declares and head
// does not still enters the union — that is the union's whole point — so
// it can still match something and be reported. Reporting it under a
// handle that names its line is honest; reporting it under an empty
// string would print a finding that names no rule at all.
func ParseHistorical(name, text string) []Rule {
	if rules, err := Parse(name, text); err == nil {
		return rules
	}
	var out []Rule
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, Rule{ID: fmt.Sprintf("unnamed-line-%d", i+1), Text: line, Line: i + 1})
	}
	return out
}
