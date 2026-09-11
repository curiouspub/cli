//go:build !race

package flow

// raceDetector says whether this binary was built with -race. See the
// file beside this one for why a probe's output has to name the
// condition it ran under.
const raceDetector = false
