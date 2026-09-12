// Command leakscan reads everything this repository has ever published —
// every blob reachable from a ref that exists on origin — and fails on
// content that says something the repository does not publish.
//
// WHY HISTORY AND NOT THE TREE. A secret removed in a later commit is
// still public in a public repository: the fix was a commit rather than a
// rewrite, so the object is still there and still reachable. The guards
// that read the working tree cannot see it, by construction, and the
// surface check reads a RANGE, which sees only what a push moved. This is
// the reader whose universe is everything.
//
// IT HAS NO OPINION ABOUT WHAT A LEAK IS. Every verdict comes from
// internal/leakcheck, handed one blob's content and the path that blob
// was seen at, and taken as given. A second definition of the rule would
// be a second thing to keep in step, and the two would drift in whichever
// direction nobody is watching.
//
// THE BASELINE IS A LEDGER AND NOT A MUTE BUTTON. This repository has
// published matches already; a scan that reds on them forever leaves two
// ways out, and both are bad — rewrite the public history of a published
// module, or loosen the rule that catches real ones. The third way is to
// record them, in a file whose every line is a reviewable diff, and to
// fail on anything not recorded. An entry that has stopped matching fails
// too, so the ledger cannot fill up with lines that mean nothing.
//
// TWO MODES AND NO DEFAULT. One reads the public manifests, needs no
// secret, and runs on a pull request from a stranger's fork like every
// other check. The other reads an operator's inventory of this project's
// own resource names, which lives outside this repository because a
// committed list of the exact names you are defending is a directory of
// them. A bare invocation meaning one of the two is a scan that silently
// ran the half nobody asked for, so neither is the default: the mode is
// named or the command refuses.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/curiouspub/cli/internal/leakcheck"
	"github.com/curiouspub/cli/internal/rulefile"
)

// What each exit code means. They are three because the third answer is
// real: "I could not look" is not "I looked and found nothing", and a
// check that collapses them reports an unfetched clone as a clean
// history.
const (
	exitClean        = 0
	exitFindings     = 1
	exitUndetermined = 2
)

// The manifests the public half of this scan reads. They are the two the
// shared engine is built from, and they are named here rather than
// restated: deleting a line from one measurably changes what this scan
// can see, which is how anybody can check it is really reading them.
const (
	citationPatternsPath = "scripts/citation-patterns.txt"
	vendorTermsPath      = "scripts/vendor-terms.txt"
	publicBaselinePath   = "scripts/leak-baseline.txt"

	// privateBaselineName is what the private half's ledger is called
	// BESIDE THE INVENTORY, which is where it has to live. A public file
	// cannot hold an exception to a private rule without becoming the
	// leak: the exception would have to name the thing it excuses.
	privateBaselineName = "leak-baseline.txt"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("leakscan", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dir := flags.String("repo", ".", "the checkout to read")
	public := flags.Bool("public", false, "scan against the public manifests in this repository")
	inventory := flags.String("inventory", "", "scan against an operator's inventory as well, read from this path")
	baselinePath := flags.String("baseline", "", "the ledger of already-published matches (defaults per mode)")
	fetch := flags.Bool("fetch", false, "fetch the universe first, with this scan's own refspec")
	record := flags.Bool("write-baseline", false, "write what this run found to the ledger instead of checking it")
	if err := flags.Parse(args); err != nil {
		return exitUndetermined
	}

	started := time.Now()
	r := repo{dir: *dir}

	rules, ledgerPath, err := vocabulary(r, *public, *inventory, *baselinePath)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return exitUndetermined
	}

	if *fetch {
		if err := r.Fetch(); err != nil {
			fmt.Fprintf(stderr, "the universe could not be fetched: %v\n", err)
			return exitUndetermined
		}
	}

	refs, err := r.Refs()
	if err != nil {
		fmt.Fprintf(stderr, "the universe could not be enumerated: %v\n", err)
		return exitUndetermined
	}
	if len(refs) == 0 {
		fmt.Fprintf(stderr, "this checkout carries no ref under %s, so there is no history "+
			"to read.\nThat is not a pass: a scan over nothing is green for the same reason a "+
			"clean one is. Fetch the universe first (%s), or run with -fetch.\n",
			strings.Join(universeRefs, " or "), strings.Join(FetchRefspec, " "))
		return exitUndetermined
	}

	blobs, err := r.Blobs(refs)
	if err != nil {
		fmt.Fprintf(stderr, "the history could not be read: %v\n", err)
		return exitUndetermined
	}
	enumerated := time.Since(started)

	found, read, skipped, bytesRead, err := scan(r, rules, blobs)
	if err != nil {
		fmt.Fprintf(stderr, "the history could not be read: %v\n", err)
		return exitUndetermined
	}
	matchedAt := time.Now()
	matched := matchedAt.Sub(started)

	// THE FLOOR. Both halves above can succeed and produce nothing, and a
	// run that read no content is evidence about nothing — which must
	// never be spelled the same way as a run that read everything and
	// found it clean.
	if read == 0 {
		fmt.Fprintf(stderr, "%d ref(s) and %d blob(s) were enumerated and not one was read, "+
			"so this run says nothing about anything.\n", len(refs), len(blobs))
		return exitUndetermined
	}

	fmt.Fprintf(stdout, "%d ref(s), %d blob(s), %d read (%d skipped as binary), %d byte(s)\n",
		len(refs), len(blobs), read, skipped, bytesRead)
	if skipped > 0 {
		fmt.Fprintf(stdout, "%d NUL-containing blob(s) were not examined; UTF-16 text is "+
			"included in that skipped count.\n", skipped)
	}
	fmt.Fprintf(stdout, "enumerated in %s, read and matched in %s\n",
		enumerated.Round(time.Millisecond), (matched - enumerated).Round(time.Millisecond))

	ledger, err := loadBaseline(ledgerPath)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return exitUndetermined
	}
	if *inventory != "" {
		// THE PRIVATE LEDGER OWNS ONLY THE PRIVATE DELTA. Public matches
		// already have one home, in the committed public ledger. Loading
		// that ledger and removing its identities here prevents a private
		// recording from making a second copy that silently diverges.
		publicLedger := filepath.Join(r.dir, filepath.FromSlash(publicBaselinePath))
		publicEntries, err := loadBaseline(publicLedger)
		if err != nil {
			fmt.Fprintf(stderr, "%v\n", err)
			return exitUndetermined
		}
		found = withoutRecorded(found, publicEntries)
	}

	if *record {
		producer := "go run ./tools/leakscan -public -write-baseline"
		if *inventory != "" {
			producer = "go run ./tools/leakscan -inventory <path> -write-baseline"
		}
		return writeLedger(stdout, stderr, ledgerPath, found, producer)
	}
	return check(r, refs, stdout, ledgerPath, ledger, found, started, matchedAt)
}

// vocabulary builds the rules for the mode that was asked for, and
// refuses when no mode was.
//
// THE REFUSAL IS THE POINT rather than a missing default. The two halves
// answer different questions and only one of them can run without a
// secret; a bare invocation that picked one would be a scan that silently
// ran the half nobody asked for, and the half it picked would be the one
// that passes.
func vocabulary(r repo, public bool, inventory, baselinePath string) (leakcheck.Rules, string, error) {
	switch {
	case public && inventory != "":
		return leakcheck.Rules{}, "", fmt.Errorf("-public and -inventory are two different " +
			"scans and this command runs one of them. The public half is the gate; the " +
			"private half is an operator's, and its findings are recorded beside its inventory")
	case !public && inventory == "":
		return leakcheck.Rules{}, "", fmt.Errorf("no mode was named, and there is no default. " +
			"Pass -public for the manifests this repository publishes, or -inventory <path> " +
			"for an operator's own list of resource names — which lives outside this " +
			"repository, because a committed list of the exact names you are defending is a " +
			"directory of them")
	}

	patterns, err := manifest(r, citationPatternsPath)
	if err != nil {
		return leakcheck.Rules{}, "", err
	}
	terms, err := manifest(r, vendorTermsPath)
	if err != nil {
		return leakcheck.Rules{}, "", err
	}
	ids := map[string]string{}
	for _, group := range []struct {
		path  string
		rules []rulefile.Rule
	}{
		{citationPatternsPath, patterns},
		{vendorTermsPath, terms},
	} {
		for _, rule := range group.rules {
			if first, exists := ids[rule.ID]; exists {
				return leakcheck.Rules{}, "", fmt.Errorf("the rule id %s belongs to both %s and "+
					"%s. A finding is a blob and a rule id, so those two rules would have one "+
					"identity", rule.ID, first, group.path)
			}
			ids[rule.ID] = group.path
		}
	}

	ledgerPath := filepath.Join(r.dir, filepath.FromSlash(publicBaselinePath))
	if inventory != "" {
		// REFUSING WHEN IT CANNOT LOOK, and this is where that principle
		// belongs — on the target that has something to look with. A run
		// asked for the private half and handed nothing to read it with
		// has not found the estate clean; it has not looked at it.
		data, err := os.ReadFile(inventory)
		if err != nil {
			return leakcheck.Rules{}, "", fmt.Errorf("the inventory at %s could not be read: "+
				"%w\nThis run was asked for the private half and cannot perform it. That is "+
				"not a clean estate, it is an unread one", inventory, err)
		}
		declared, err := rulefile.Parse(inventory, string(data))
		if err != nil {
			return leakcheck.Rules{}, "", err
		}
		if len(declared) == 0 {
			return leakcheck.Rules{}, "", fmt.Errorf("the inventory at %s declares no rules, "+
				"so the private half of this scan would pass everything silently", inventory)
		}
		for _, rule := range declared {
			if first, exists := ids[rule.ID]; exists {
				return leakcheck.Rules{}, "", fmt.Errorf("the inventory rule id %s already "+
					"belongs to %s. A finding is a blob and a rule id, so the private match "+
					"could not be distinguished from the public one", rule.ID, first)
			}
		}
		patterns = append(patterns, declared...)
		ledgerPath = defaultPrivateBaseline(baselinePath, inventory)
	}
	if baselinePath != "" {
		ledgerPath = baselinePath
	}

	rules, err := leakcheck.New(patterns, terms)
	if err != nil {
		return leakcheck.Rules{}, "", err
	}
	if rules.Empty() {
		return leakcheck.Rules{}, "", fmt.Errorf("the vocabulary declares %d pattern(s) and "+
			"%d term(s) — with either at zero this scan would pass everything silently",
			len(patterns), len(terms))
	}
	return rules, ledgerPath, nil
}

// defaultPrivateBaseline puts the private ledger beside the inventory it
// belongs to, which is outside this repository.
func defaultPrivateBaseline(given, inventory string) string {
	if given != "" {
		return given
	}
	return filepath.Join(filepath.Dir(inventory), privateBaselineName)
}

// manifest reads one rule file from the checkout being scanned, strictly.
func manifest(r repo, name string) ([]rulefile.Rule, error) {
	path := filepath.Join(r.dir, filepath.FromSlash(name))
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", name, err)
	}
	declared, err := rulefile.Parse(name, string(data))
	if err != nil {
		return nil, err
	}
	if len(declared) == 0 {
		return nil, fmt.Errorf("%s declares no rules, so this scan would pass everything "+
			"silently", name)
	}
	return declared, nil
}

// scan reads every blob and asks the engine about it once per path it was
// seen at.
//
// ONCE PER PATH, and the answers are unioned. Whether a line is a rule or
// a violation depends on the file it sits in, so the same bytes can be a
// finding under one name and exempt under another — and content published
// at a path where it is a finding is published, whatever else it was also
// called.
func scan(r repo, rules leakcheck.Rules, blobs []blob) (map[finding]map[string]bool, int, int, int64, error) {
	found := map[finding]map[string]bool{}
	shas := make([]string, 0, len(blobs))
	paths := map[string][]string{}
	for _, b := range blobs {
		shas = append(shas, b.SHA)
		paths[b.SHA] = b.Paths
	}

	read, skipped := 0, 0
	var bytesRead int64
	err := r.contents(shas, func(sha string, content []byte) error {
		// A BLOB WITH A NUL BYTE IS NOT TEXT, and the engine reads lines.
		// The count is reported rather than swallowed: a blob nobody read
		// is not a blob that came back clean.
		if bytes.IndexByte(content, 0) >= 0 {
			skipped++
			return nil
		}
		read++
		bytesRead += int64(len(content))
		text := string(content)
		for _, path := range paths[sha] {
			for _, m := range rules.Check(path, text) {
				f := finding{Blob: sha, PatternID: m.PatternID}
				if found[f] == nil {
					found[f] = map[string]bool{}
				}
				found[f][path] = true
			}
		}
		return nil
	})
	return found, read, skipped, bytesRead, err
}

// check compares what was found against the ledger and decides the exit
// code.
func check(r repo, refs []string, stdout io.Writer, ledgerPath string, ledger []entry,
	found map[finding]map[string]bool, started, matched time.Time) int {

	recorded := map[finding]entry{}
	for _, e := range ledger {
		recorded[e.finding] = e
	}

	var unrecorded []finding
	for f := range found {
		if _, ok := recorded[f]; !ok {
			unrecorded = append(unrecorded, f)
		}
	}
	sort.Slice(unrecorded, func(i, j int) bool {
		if unrecorded[i].Blob != unrecorded[j].Blob {
			return unrecorded[i].Blob < unrecorded[j].Blob
		}
		return unrecorded[i].PatternID < unrecorded[j].PatternID
	})

	// A STALE ENTRY IS A FAILURE. An entry whose blob is no longer
	// reachable, or which no longer matches the rule it names, has
	// stopped meaning anything — and a ledger that accumulates lines
	// nobody can check is a ledger nobody reads.
	var stale []entry
	for _, e := range ledger {
		if _, ok := found[e.finding]; !ok {
			stale = append(stale, e)
		}
	}

	var wanted []string
	for _, f := range unrecorded {
		wanted = append(wanted, f.Blob)
	}
	attribution, err := r.Attribute(refs, wanted)
	if err != nil {
		// NOT FATAL, because the finding is the finding. Attribution is a
		// report ABOUT a finding rather than part of it, and throwing the
		// finding away because the story about it could not be assembled
		// would be the report deciding what the measurement was.
		fmt.Fprintf(stdout, "attribution could not be established: %v\n", err)
		attribution = map[string]string{}
	}
	if len(wanted) > 0 {
		fmt.Fprintf(stdout, "attributed %d finding(s) in %s\n", len(wanted),
			time.Since(matched).Round(time.Millisecond))
	}

	for _, f := range unrecorded {
		where := strings.Join(sortedPaths(found[f]), ", ")
		earliest := "no commit in the universe contains it, which should be impossible"
		if sha, ok := attribution[f.Blob]; ok {
			earliest = "earliest commit containing it: " + short(sha)
		}
		fmt.Fprintf(stdout, "%s %s — seen at %s — %s\n", short(f.Blob), f.PatternID, where, earliest)
	}
	for _, e := range stale {
		fmt.Fprintf(stdout, "%s:%d records %s and nothing matches it now\n",
			ledgerPath, e.Line, e.finding)
	}

	if len(unrecorded) > 0 {
		fmt.Fprintf(stdout, "\nThese are published. A commit that removed one did not "+
			"unpublish it — the object is still reachable, and this is what a stranger "+
			"cloning the repository gets. If a match is one this project has decided to "+
			"live with, record it in %s with the reason it stays; if it is not, it needs a "+
			"decision rather than a line.\n", ledgerPath)
	}
	if len(stale) > 0 {
		fmt.Fprintf(stdout, "\nAn entry that matches nothing is an exception nobody can "+
			"check. Either the rule it names was retired, or the object stopped being "+
			"reachable, or the line was wrong when it was written — and the ledger cannot "+
			"tell you which, which is why it refuses to carry it.\n")
	}
	if len(unrecorded) > 0 || len(stale) > 0 {
		return exitFindings
	}

	fmt.Fprintf(stdout, "clean against %d baselined finding(s), in %s.\n", len(ledger),
		time.Since(started).Round(time.Millisecond))
	return exitClean
}

// writeLedger records what a run found instead of checking it.
//
// IT NEVER EXITS CLEAN, and that is deliberate. This flag is how the
// first ledger gets written — the list is produced by running the scan
// rather than by remembering — and a run that RECORDED findings has not
// checked anything. Spelling that as a pass would make "write the
// baseline" a way to turn the gate green.
func writeLedger(stdout, stderr io.Writer, ledgerPath string, found map[finding]map[string]bool,
	producer string) int {
	entries := make([]entry, 0, len(found))
	for f, paths := range found {
		entries = append(entries, entry{
			finding: f,
			// EVERY REASON IS THE SAME ONE TODAY and it is written out
			// rather than implied: the match is not confined to the
			// working tree, so removing it would mean rewriting published
			// history rather than editing a file. A reason somebody
			// disagrees with is a reason they can argue with in a diff.
			Reason: "rewrite-ineligible",
			Paths:  sortedPaths(paths),
		})
	}
	header := `# Matches this repository has ALREADY PUBLISHED, and the reason each stays.
#
# A finding is a BLOB and a RULE: not a commit, not a path, not a line.
# The same content at another path under the same rule is the same
# finding; a different rule firing on the same content is a new one.
#
# Four fields: the blob, the rule's id, the reason it stays, and the
# comma-separated paths it was seen at when this line was written. The
# paths are CONTEXT — they are what makes a line readable a year later —
# and they are not part of what is compared.
#
# THERE IS NO FIELD FOR THE TEXT THAT MATCHED, and there never will be.
# This file records that a match is KNOWN; writing the string it matched
# would publish the thing being recorded, in a public file, for ever — a
# directory of exactly what is being defended. The blob hash re-finds it
# and discloses nothing.
#
# Adding a line is a reviewable diff, which is the whole difference
# between a ledger and a loosened rule. An entry that stops matching
# FAILS, so this file cannot fill up with lines that mean nothing.
#
# Produced by: ` + producer + "\n"
	if err := os.WriteFile(ledgerPath, []byte(renderBaseline(header, entries)), 0o644); err != nil {
		fmt.Fprintf(stderr, "writing %s: %v\n", ledgerPath, err)
		return exitUndetermined
	}
	fmt.Fprintf(stdout, "recorded %d finding(s) in %s.\n", len(entries), ledgerPath)
	fmt.Fprintln(stdout, "This run RECORDED and did not check, so it is not a pass. Read the "+
		"diff — every line is a match that is already public — and run again without the flag.")
	return exitUndetermined
}

func withoutRecorded(found map[finding]map[string]bool, recorded []entry) map[finding]map[string]bool {
	out := make(map[finding]map[string]bool, len(found))
	for f, paths := range found {
		out[f] = paths
	}
	for _, e := range recorded {
		delete(out, e.finding)
	}
	return out
}

func sortedPaths(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
