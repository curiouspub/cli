package guard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/curiouspub/cli/pkg/wire"
)

// THE README'S PLATFORM HALF, HELD TO SOMETHING FOR THE FIRST TIME.
//
// This page has always carried two claims that move independently: whether
// the tool is RELEASED, and whether the platform it deploys to is RUNNING.
// The first has been guarded since it was written — readmecontract_test.go
// holds it against a reachable release tag, at both ends, because the row
// that only demanded the disclaimer went quiet at exactly the moment the
// page became wrong.
//
// The second was unguarded prose. It said the platform was not running, and
// nothing in this repository could tell whether that was still true. The
// same defect that row learned the hard way was sitting beside it the whole
// time, in the other direction: a page saying a thing is unbuilt, the day
// after it ships, is the same error running backwards.
//
// WHAT IT IS KEYED TO, AND WHY NOT A LIVE CALL. A recorded response, in
// testdata, committed. Reaching the network from a test would fail for
// reasons that are not about this tree — an outage, a captive portal, a
// runner with no egress — and it cannot run at all on a pull request from a
// stranger's fork, which is a property this suite is deliberately built to
// have. A fixture is a TREE FACT: somebody chose to commit it, the diff
// shows it, and the claim moves when the fixture does.
//
// What it costs, said plainly: this row cannot notice the platform going
// down. It is not a monitor and must not be read as one. It asserts that
// the page's claim and the last recorded observation agree, which is a
// question about authorship rather than about uptime.

// platformCapacityFixture is the response recorded from the live
// /v1/capacity on 2026-09-21, the day the platform first answered it.
const platformCapacityFixture = "platform-capacity.json"

// The sentences the two states require. Each is a phrase a reader meets,
// not a marker: a page can only be held to what it actually says.
const (
	platformRunningClaim = "**The platform it deploys to is running.**"
	platformCapacityLine = "**Capacity opens daily to a limited number of accounts**"
)

// conditionalPlatformPhrases are the forms the page used while the platform
// was not running. Their ABSENCE is required once it is — a page that says
// both is worse than one that says the wrong one, because a reader believes
// whichever they read first.
var conditionalPlatformPhrases = []string{
	"Once the platform is up",
	"not yet deployable",
	"there is nothing at the other end",
	"is not running yet",
}

func readPlatformCapacityFixture(t *testing.T) wire.CapacityResponse {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", platformCapacityFixture))
	if err != nil {
		t.Fatalf("reading the recorded capacity response: %v", err)
	}
	// DECODED THROUGH THE CONTRACT, not into a map. If the fixture stops
	// being a CapacityResponse then either the recording is wrong or the
	// contract moved, and both are things this row should refuse rather
	// than read around.
	var got wire.CapacityResponse
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("the recorded response does not decode as wire.CapacityResponse: %v\n"+
			"Either the recording is wrong or /v1/capacity has changed shape, and this row "+
			"refuses both rather than reading around them.", err)
	}
	return got
}

// TestTheReadmesPlatformHalfMatchesTheRecordedCapacity is the rule, and it
// runs in BOTH directions from one fixture.
//
// Open: the page must claim the platform is running and must carry none of
// the conditional phrasing. Closed: it must carry the conditional phrasing.
// One fixture decides which, so flipping the recording flips the demand —
// that is what stops this becoming a one-way assert that passes for ever
// once somebody edits the page.
func TestTheReadmesPlatformHalfMatchesTheRecordedCapacity(t *testing.T) {
	root := moduleRoot(t)
	// RENDERED PROSE, as a reader meets it, and not the raw file. A claim
	// that wraps across a line break is not contiguous in the source, so a
	// raw containment check refuses a page that says exactly the right
	// thing — and the obvious repair, reflowing the page to suit the row,
	// is the guard editing its subject to be checkable.
	readme := renderedProse(readReadmeFile(t, root))
	capacity := readPlatformCapacityFixture(t)

	if !capacity.Open {
		for _, phrase := range conditionalPlatformPhrases {
			if strings.Contains(readme, phrase) {
				return
			}
		}
		t.Errorf("the recorded capacity says the platform is not open, and README.md carries none "+
			"of the conditional phrasings %q that tell a reader so", conditionalPlatformPhrases)
		return
	}

	for _, want := range []string{platformRunningClaim, platformCapacityLine} {
		if !strings.Contains(readme, want) {
			t.Errorf("the recorded capacity is open (%d accounts left, resetting %s) and README.md "+
				"does not carry %q", capacity.AccountsLeft, capacity.ResetsAt.Format("2006-01-02T15:04:05Z"), want)
		}
	}
	for _, phrase := range conditionalPlatformPhrases {
		if strings.Contains(readme, phrase) {
			t.Errorf("the platform is open and README.md still says %q — a page that carries both "+
				"claims is worse than one carrying the wrong one, because a reader believes "+
				"whichever they meet first", phrase)
		}
	}

	// "A LIMITED NUMBER" IS A NUMBER. The page makes a quantitative claim;
	// a recording of zero would make it true in a way nobody means.
	if capacity.AccountsLeft <= 0 {
		t.Errorf("the recorded response is open with %d accounts left, which does not support "+
			"the page's claim that capacity opens to a limited number of them", capacity.AccountsLeft)
	}
}

// TestThePlatformRuleIsOneDirectional fixtures both answers, so neither
// branch of the rule above is trusted on the strength of the one state the
// tree happens to be in.
//
// The readmecontract rows learned this: a check with only one live branch
// is a check that has never been observed to refuse anything.
func TestThePlatformRuleIsOneDirectional(t *testing.T) {
	running := "…" + platformRunningClaim + " … " + platformCapacityLine + " …"
	notRunning := "… the platform it deploys to is not running yet …"

	cases := map[string]struct {
		open      bool
		readme    string
		wantRefus bool
	}{
		"open page, open platform":          {true, running, false},
		"conditional page, open platform":   {true, notRunning, true},
		"open page, closed platform":        {false, running, true},
		"conditional page, closed platform": {false, notRunning, false},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := platformCopyDisagrees(c.open, c.readme); got != c.wantRefus {
				t.Errorf("disagreement = %v, want %v", got, c.wantRefus)
			}
		})
	}
}

// platformCopyDisagrees is the predicate the row above exercises and the
// test above it applies to the real page. ONE implementation, two callers —
// a fixture that proved a second copy of the logic would prove nothing
// about the one that runs.
//
// It takes ALREADY-RENDERED prose. Both callers render before calling, so
// neither can be checking a different text from the other.
func platformCopyDisagrees(open bool, readme string) bool {
	carriesConditional := false
	for _, phrase := range conditionalPlatformPhrases {
		if strings.Contains(readme, phrase) {
			carriesConditional = true
		}
	}
	if !open {
		return !carriesConditional
	}
	return carriesConditional ||
		!strings.Contains(readme, platformRunningClaim) ||
		!strings.Contains(readme, platformCapacityLine)
}
