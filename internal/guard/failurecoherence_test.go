package guard

import (
	"sort"
	"strings"
	"testing"
)

// THE ID-COHERENCE ROW: every site carrying a given failure id carries the
// same next action.
//
// # Why the census cannot see this
//
// The census counts CONSTRUCTIONS and compares that count against the
// checker's. Both numbers can be right while this property is broken,
// because the defect is not a missing site — it is a RELATION BETWEEN
// sites that agree on their id and disagree on their remedy. Nothing that
// counts can measure agreement.
//
// It is not hypothetical. `client-request-rejected` was carried by three
// production sites holding two different recovery contracts: a repeated
// authentication refusal answered with FreshDeploy and a clock remedy, and
// a rejected request answered with GiveUp and an update-and-report remedy.
// Both cold reviews of this guard ran while that was true; the census was
// green throughout, and correctly so. A reader who looked the id up would
// have been handed two different fixes with nothing to choose between them.
//
// # What the card says this enforces
//
// A family is the same diagnosed condition AND THE SAME RECOVERY CONTRACT.
// The id is the name of the family, so two sites sharing an id and not an
// action are either one family with a wrong action at one of them, or two
// families wearing one name. Both are defects and this row does not care
// which: it reds and names BOTH sites, because the fix requires seeing the
// pair and neither one alone tells you which way to go.
func TestEverySiteCarryingAnIdCarriesTheSameAction(t *testing.T) {
	obs := contractObligations(t)

	idAt, actionAt := map[string]string{}, map[string]string{}
	for _, o := range obs {
		if len(o.values) == 0 {
			continue // unresolved, or discharged by the obligation ledger
		}
		switch o.field {
		case "ID":
			idAt[o.where] = strings.Trim(o.values[0], `"`)
		case "Next":
			vals := append([]string(nil), o.values...)
			sort.Strings(vals)
			actionAt[o.where] = strings.Join(vals, "|")
		}
	}

	// id -> action -> the sites carrying that pairing.
	byID := map[string]map[string][]string{}
	var paired int
	for site, id := range idAt {
		action, ok := actionAt[site]
		if !ok || id == "" {
			continue
		}
		paired++
		if byID[id] == nil {
			byID[id] = map[string][]string{}
		}
		byID[id][action] = append(byID[id][action], site)
	}

	// AN EMPTY COMPARISON MUST NOT READ AS AGREEMENT. If pairing fell
	// through — a refactor that renames a field, a resolver that stops
	// resolving ids — every id would trivially "agree" and this row would
	// be a green light over an unmeasured property.
	if paired == 0 {
		t.Fatal("no site produced both an id and an action, so this row " +
			"compared nothing and its green means nothing")
	}

	var ids []string
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		actions := byID[id]
		if len(actions) < 2 {
			continue
		}
		var groups []string
		var keys []string
		for a := range actions {
			keys = append(keys, a)
		}
		sort.Strings(keys)
		for _, a := range keys {
			sites := append([]string(nil), actions[a]...)
			sort.Strings(sites)
			groups = append(groups, "  "+a+" at "+strings.Join(sites, ", "))
		}
		t.Errorf("id %q is carried with %d different next actions:\n%s\n"+
			"A family is the same diagnosed condition AND the same recovery "+
			"contract. Either one of these sites has the wrong action, or "+
			"these are two families sharing one name — split the id or "+
			"correct the action, and record it as a product decision.",
			id, len(actions), strings.Join(groups, "\n"))
	}

	// THE SUMMARY MUST NOT OUTRANK THE VERDICT. Written as an
	// unconditional "every id coherent", this line printed a clean bill
	// directly underneath its own failure — the defect 71b93c4 fixed in
	// the census row, reintroduced here by its author eight commits later
	// and caught by the mutation rather than by reading.
	if t.Failed() {
		t.Logf("%d sites paired across %d ids; the disagreements above are the finding",
			paired, len(byID))
		return
	}
	t.Logf("%d sites paired across %d ids, every id coherent", paired, len(byID))
}

// contractObligations runs the contract checker for its ENUMERATION only,
// under a throwaway T so its assertions stay on its own row rather than
// this one. Same reasoning as collectedSiteCount: what is shared is the
// checker's resolved values, which is the thing being examined; what is
// not shared is the verdict.
func contractObligations(t *testing.T) []obligation {
	t.Helper()
	before := failureContractObligations
	failureContractObligations = nil
	TestTheFailureContractHolds(&testing.T{})
	got := failureContractObligations
	if len(got) == 0 {
		failureContractObligations = before
		t.Fatal("the contract checker produced no obligation, so this row " +
			"would compare nothing")
	}
	return got
}
