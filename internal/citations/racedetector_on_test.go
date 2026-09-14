//go:build race

package citations_test

// raceDetector says whether this test binary was built with -race, so a
// logged ratio names the condition it was taken under. The gate runs this
// package both ways, and the reason a probe's line has to say which is
// recorded beside the same pair of files in internal/flow.
//
// THE PAIR IS COMPILED BOTH WAYS BY THE GATE, which keeps a build-tagged
// file from going dark: the plain pass builds the !race half and the race
// pass builds this one.
const raceDetector = true
