//go:build race

package flow

// raceDetector says whether this binary was built with -race, and it
// exists because the stall windows in internal/timing are margins over a
// gap that the detector MOVES.
//
// Measured on darwin, 2026-09-10, the same probe over the same fixture:
// 42.1 ms without the detector and 126.2 ms with it, on a run of 140.
// make ci runs internal/flow twice, once each way, so a margin has to
// hold in both — which means the number recorded is the worse one, and
// an operator reading a probe's paste-ready line has to be able to tell
// which of the two conditions produced it. Without this the two runs
// print identical-looking lines carrying numbers that differ threefold.
//
// THE PAIR OF FILES IS COMPILED BOTH WAYS BY THE GATE, which is what
// keeps a build-tagged file from going dark: the plain pass builds the
// !race half and the race pass builds this one.
const raceDetector = true
