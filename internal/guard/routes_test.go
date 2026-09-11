package guard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// versionedPathPrefix is how every route of this API begins, and it is
// the WHOLE of what this guard looks for.
//
// A path is the one part of a call a client cannot get from anywhere
// else: the method, the body and the headers all come from types and
// helpers, and the path is a string somebody types. So a file spelling
// one is a file making a request the route table does not know about,
// whatever the rest of it looks like.
const versionedPathPrefix = "/v1/"

// routeOwningPackage is where every path this client sends lives.
const routeOwningPackage = "internal/api"

// TestTheAgentSurfaceNamesNoRoute.
//
// # The failure it prevents is a tool inventing an endpoint
//
// There is no endpoint that answers a deploy's current state — the
// surface has a create, a start, a publish and an event stream, and
// nothing that reads a record back. The tempting repair, for anybody
// writing a tool whose whole job is to report a status, is a bare GET
// at the deploy's own path: it reads naturally, it is one line, and it
// fails at RUN TIME against a route nobody serves, so nothing in a build
// says no.
//
// Stating the rule as "this package spells no route" rather than "this
// package does not spell that one" is deliberate. A guard naming the
// single endpoint somebody happened to think of first is a guard the
// next invention walks past, and the class is not a broader claim than
// the evidence supports: the agent surface has no business naming a path
// at all, because every call it makes goes through the package that owns
// the route table.
//
// # Both directions, because a guard that finds nothing looks like a clean tree
//
// It fails if it scanned no file, like every guard here. And it asserts
// that the package which SHOULD spell routes still does, because a check
// for the absence of a string is satisfied perfectly by a matcher that
// has stopped matching — and this one's whole job is an absence.
//
// REQUIRED MUTATION, run 2026-09-11: see the commit message. Both
// directions were run — a path planted in the agent surface, and the
// scan pointed at a package that does not exist.
func TestTheAgentSurfaceNamesNoRoute(t *testing.T) {
	root := moduleRoot(t)

	agentPrefix := filepath.Join(root, filepath.FromSlash(agentSurfacePackage)) + string(filepath.Separator)
	routePrefix := filepath.Join(root, filepath.FromSlash(routeOwningPackage)) + string(filepath.Separator)

	var scanned, routesWhereTheyBelong int
	for _, path := range publishedTextFiles(t, root) {
		underAgent := strings.HasPrefix(path, agentPrefix)
		underRoutes := strings.HasPrefix(path, routePrefix)
		if !underAgent && !underRoutes {
			continue
		}

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		spellsAPath := strings.Contains(string(data), versionedPathPrefix)

		if underRoutes {
			if spellsAPath {
				routesWhereTheyBelong++
			}
			continue
		}

		scanned++
		if !spellsAPath {
			continue
		}
		t.Errorf("%s spells %q.\n"+
			"Every call this client makes goes through %s, which owns the route table — "+
			"so a path here is a request nobody has checked against the contract, and the "+
			"one it would most likely be is an endpoint that does not exist. A tool that "+
			"needs a call the client cannot make is a finding to raise, not a path to type.",
			displayPath(root, path), versionedPathPrefix, routeOwningPackage)
	}

	if scanned == 0 {
		t.Fatalf("no published file under %s was scanned, so this guard measured nothing",
			agentSurfacePackage)
	}
	// THE POSITIVE CONTROL, and it is not decoration: this row's claim is
	// that a string is ABSENT, and every mechanism by which such a row
	// can break — a renamed package, a moved prefix, an enumeration that
	// returns nothing — produces exactly the same clean result. A file
	// that MUST contain the string is the only thing that can tell a
	// guard which is looking from one which is not.
	if routesWhereTheyBelong == 0 {
		t.Errorf("no file under %s spells %q either, so this scan is not finding the "+
			"string anywhere — which makes its silence about %s meaningless",
			routeOwningPackage, versionedPathPrefix, agentSurfacePackage)
	}
}
