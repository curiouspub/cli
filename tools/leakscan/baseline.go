package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// finding is one thing this scan found, and it is a PAIR: the blob and
// the rule that fired on it.
//
// NOT A COMMIT, NOT A PATH, NOT A LINE. A blob is content, and content is
// what leaks; the same blob reappearing at another path under the same
// rule is the same finding, and a different rule firing on the same blob
// is a new one. A commit is a story about how the content arrived and
// several are usually true at once; a path is a name the content wore;
// a line moves when somebody reflows a paragraph.
type finding struct {
	Blob      string
	PatternID string
}

func (f finding) String() string { return short(f.Blob) + " " + f.PatternID }

// entry is one line of a baseline: a finding that is already published,
// why it stays, and where it was seen when the line was written.
//
// THE PATHS ARE CONTEXT AND NOT IDENTITY. They are what makes an entry
// readable by a person a year later — a bare pair of hashes says nothing
// about what anybody is looking at — and they are deliberately outside
// the comparison, so a blob that turns up under one more name does not
// become a new finding.
//
// THERE IS NO FIELD FOR THE TEXT THAT MATCHED, and that is the whole
// design. A baseline records that a match is KNOWN; writing the string it
// matched would publish the thing being recorded, in a public file, for
// ever — a directory of exactly what is being defended, shipped by the
// ledger that exists to defend it. The blob hash re-finds it and
// discloses nothing.
type entry struct {
	finding
	Reason string
	Paths  []string
	Line   int
}

// loadBaseline reads a ledger. A missing file is an empty ledger and not
// an error: a repository with nothing to record should not have to carry
// an empty file to say so.
func loadBaseline(path string) ([]entry, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the baseline at %s: %w", path, err)
	}

	var out []entry
	seen := map[finding]int{}
	for i, raw := range strings.Split(string(data), "\n") {
		lineNumber := i + 1
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 4 {
			return nil, fmt.Errorf("%s:%d has %d field(s) and an entry has four: the blob, "+
				"the rule's id, the reason it stays, and the comma-separated paths it was "+
				"seen at", path, lineNumber, len(fields))
		}
		e := entry{
			finding: finding{Blob: fields[0], PatternID: fields[1]},
			Reason:  fields[2],
			Paths:   strings.Split(fields[3], ","),
			Line:    lineNumber,
		}
		if first, ok := seen[e.finding]; ok {
			return nil, fmt.Errorf("%s:%d records %s, which line %d already records. One "+
				"finding with two entries means removing one of them changes nothing, which "+
				"is how an exception outlives the reason somebody wrote it",
				path, lineNumber, e.finding, first)
		}
		seen[e.finding] = lineNumber
		out = append(out, e)
	}
	return out, nil
}

// renderBaseline writes a ledger back out, sorted, so that two runs
// produce the same file and a diff shows what changed rather than how the
// lines moved.
func renderBaseline(header string, entries []entry) string {
	sorted := append([]entry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Blob != sorted[j].Blob {
			return sorted[i].Blob < sorted[j].Blob
		}
		return sorted[i].PatternID < sorted[j].PatternID
	})
	var b strings.Builder
	b.WriteString(header)
	for _, e := range sorted {
		fmt.Fprintf(&b, "%s %s %s %s\n", e.Blob, e.PatternID, e.Reason, strings.Join(e.Paths, ","))
	}
	return b.String()
}
