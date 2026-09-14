package timing

// Ceiling is a bound on a RATIO a test measures, where an Entry bounds a
// DURATION. It lives in this package for the reason an Entry does: a
// number a row asserts against is a claim about machines, and a claim
// about machines is recorded with what stands behind it.
//
// A CEILING HAS NO FIELD FOR READINGS, and that is deliberate rather than
// unfinished. No ceiling in the tree has been read on a hosted runner, so
// every one of them is PROVISIONAL, and the guard beside this package
// refuses a ceiling that says otherwise. Re-ruling a ceiling from its
// hosted readings is the change that adds the field those readings go in.
type Ceiling struct {
	// Name is the identifier, the Ceilings key and this field: one name.
	Name string

	// Row is the test that asserts the ceiling.
	Row string

	// Governs says in words what the ratio is a ratio of.
	Governs string

	// Value is the ceiling. A measured ratio above it fails the row.
	Value float64

	// Provisional says no hosted reading stands behind Value: it is a
	// number chosen somewhere other than where it is enforced.
	Provisional bool

	// ReRuleAfter is how many hosted readings PER LEG re-rule a
	// provisional ceiling. Zero is nobody having said when it stops being
	// provisional, and the guard reds on it.
	ReRuleAfter int

	// Why is what a provisional value was sized from, so a reader can see
	// how little stands behind it.
	Why string
}

// TokeniserCost bounds how much longer the identifier tokeniser takes on
// an input four times the size, on the worst input it has.
var TokeniserCost = Ceiling{
	Name: "TokeniserCost",
	Row:  "TestTheCostOfTokenisingIsBoundedByTheBound",
	Governs: "the best-of-seven cost of tokenising a line of alternating case " +
		"four times as long, over the cost of the shorter line",
	// 16, PROVISIONAL AND UNMEASURED, 2026-09-14. It was sized on the
	// machine the row was written on, where bounded ratios ran 4.0 to 4.2
	// across ten trials and the unbounded tokeniser reached 56, and the
	// row's comment then said an ordinary bad minute on a shared runner
	// could not reach it. That was a claim made from no measurement. The
	// row's first red in 150 CI runs arrived on a hosted Windows runner,
	// 16.9 against 16, on a pull request that did not touch the package,
	// over a smaller input that cost 507 µs there.
	//
	// The inputs are now milliseconds and every run logs its ratio. The
	// value is re-ruled from the first 20 hosted readings per leg, under
	// the rule StreamGoesQuiet's record keeps: the maximum over valid
	// readings, and no valid reading held beside it because it is large.
	Value:       16,
	Provisional: true,
	ReRuleAfter: 20,
	Why: "sized on one development machine (bounded ratios 4.0 to 4.2 over ten " +
		"trials, unbounded 56); no reading from a hosted runner",
}

// Ceilings is every ceiling a test asserts. A ceiling declared and not
// listed here is one the guard cannot see, and it reds on that.
var Ceilings = map[string]*Ceiling{
	"TokeniserCost": &TokeniserCost,
}
