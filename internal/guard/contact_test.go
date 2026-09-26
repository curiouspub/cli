package guard

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// mailboxMention is a mailbox at this project's domain, in any of the
// spellings people use to keep an address away from harvesters: an @, a
// bracketed or parenthesised "at", a dashed "-a-" or "-at-", or the word.
// It names the local parts a contact address plausibly has, so ordinary
// prose ("the site at the domain") is not mistaken for an address.
var mailboxMention = regexp.MustCompile(
	`(?i)\b(hello|support|abuse|contact|info|help|security|noreply)\s*(@|\[at\]|\(at\)|-a-|-at-|\s+at\s+)\s*curious\.pub\b`)

// writtenContacts are the only two ways an address is written anywhere
// this repository publishes: support for testers, abuse for reports. Both
// are written out in words, as the project's site writes them.
var writtenContacts = map[string]bool{
	"support at curious.pub": true,
	"abuse at curious.pub":   true,
}

// TestContactAddressesAreWrittenOneWay holds every published file to one
// spelling of each contact address. The address was once spelled several
// ways across this project's surfaces, including a mailbox that is no
// longer named anywhere, and a reader cannot tell which of several
// spellings is the one that is read.
func TestContactAddressesAreWrittenOneWay(t *testing.T) {
	root := moduleRoot(t)
	seen := map[string]int{}
	for _, path := range publishedTextFiles(t, root) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		rel, _ := filepath.Rel(root, path)
		for _, m := range mailboxMention.FindAllString(string(data), -1) {
			if writtenContacts[m] {
				seen[m]++
				continue
			}
			t.Errorf("%s names %q\nA contact address is written as one of %v and no other way.",
				filepath.ToSlash(rel), m, keysOf(writtenContacts))
		}
	}
	// THE SUBJECT EXISTS. With no address found at all, "no second
	// spelling" and "read nothing" would be the same green.
	for want := range writtenContacts {
		if seen[want] == 0 {
			t.Errorf("no published file names %q, so this row cannot tell a clean tree from one it never read", want)
		}
	}
}

// TestTheContactRuleSeesBothDirections drives the pattern against text
// written here. The samples are assembled from parts, so this file never
// contains a spelling the row above refuses: the rule reads this file too,
// and exempting it would make the one file that spells every refused form
// the one file nothing checks.
func TestTheContactRuleSeesBothDirections(t *testing.T) {
	const domain = "curious" + ".pub"
	for _, bad := range []string{
		"write to hello" + " at " + domain, "[hello" + " -a- " + domain + "]", "hello" + "-at-" + domain,
		"support" + "@" + domain, "abuse" + " [at] " + domain, "Support" + " At " + domain,
	} {
		m := mailboxMention.FindString(bad)
		if m == "" || writtenContacts[m] {
			t.Errorf("the rule passes %q", bad)
		}
	}
	for _, fine := range []string{
		"write to support at " + domain + ".", "mail abuse at " + domain + " instead",
		"the site at " + domain, "deployed at " + domain + " today",
	} {
		if m := mailboxMention.FindString(fine); m != "" && !writtenContacts[m] {
			t.Errorf("the rule refuses %q (matched %q)", fine, m)
		}
	}
}

func keysOf(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
