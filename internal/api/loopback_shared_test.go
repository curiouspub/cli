package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// The set of hosts this project will speak plaintext to is enforced in
// two languages: here, by the address guard, and in the npm wrapper's
// install script, which applies the same rule to the origin its
// download override may name.
//
// ONE FILE IS THE SOURCE AND THIS ROW IS WHAT KEEPS IT ONE. The wrapper
// reads that file at run time, so it cannot drift; the map below is Go
// source and can. Without this row the two would be two rules wearing
// one name, and the day they disagreed nothing would say so — the
// client would refuse an address the wrapper had just accepted, or the
// reverse, and both look like a bug in whichever half you are reading.
//
// The file lives beside the wrapper rather than beside this package,
// and that is forced rather than chosen: a package's allowlist of
// published files is relative to the package root, so a copy living
// here could not ship to anyone installing the wrapper.
func TestLoopbackSetIsTheOneTheWrapperShips(t *testing.T) {
	root := moduleRootFrom(t)
	path := filepath.Join(root, "npm", "loopback-hosts.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v\n"+
			"This row cannot establish what the wrapper allows, so it fails rather than "+
			"report a pass over a set it never read.", path, err)
	}

	var shipped struct {
		Hosts []string `json:"hosts"`
	}
	if err := json.Unmarshal(data, &shipped); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	if len(shipped.Hosts) == 0 {
		t.Fatal("the shipped loopback set is empty — this row would compare two nothings")
	}

	live := make([]string, 0, len(insecureLoopbackHosts))
	for host := range insecureLoopbackHosts {
		live = append(live, host)
	}
	sort.Strings(live)

	wanted := append([]string(nil), shipped.Hosts...)
	sort.Strings(wanted)

	if len(live) != len(wanted) {
		t.Fatalf("the client allows %v and the wrapper ships %v", live, wanted)
	}
	for i := range live {
		if live[i] != wanted[i] {
			t.Fatalf("the client allows %v and the wrapper ships %v", live, wanted)
		}
	}
}

// moduleRootFrom walks up from this package's own directory until it
// finds the module's go.mod.
func moduleRootFrom(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("walked up to the filesystem root without finding a go.mod")
		}
		dir = parent
	}
}
