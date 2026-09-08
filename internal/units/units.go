// Package units is how this program writes a number for somebody to
// read: a size in bytes, and a count of things.
//
// IT IS A LEAF, AND THAT IS THE WHOLE REASON IT IS A PACKAGE. The first
// caller is the local limits, whose every message states a measurement
// and restates the limit it broke; the next is the agent-facing tool
// description, which has to state the same limits in the same words. A
// formatter living inside the package that measures would be reachable
// only by importing a file walker to render a string, so the second
// caller would write its own — and two spellings of one size is exactly
// the disagreement this package exists to prevent.
//
// DECIMAL SI, NOT BINARY. A kilobyte here is 1,000 bytes and a megabyte
// is 1,000,000. The limits this renders against are decimal, and the gap
// between the two readings is about 5% — which surfaces nowhere except
// at the boundary, as a refusal whose numbers the reader cannot make
// agree. One reading, everywhere, is worth more than the familiarity of
// the other.
package units

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// prefixes are the decimal SI steps, smallest first. The list stops at
// exabytes because an int64 cannot reach a zettabyte.
var prefixes = [...]string{"kB", "MB", "GB", "TB", "PB", "EB"}

// Bytes renders a byte count with one decimal place: "5.2 MB".
//
// ONE DECIMAL, ALWAYS, above a kilobyte. It is enough to tell 5.2 from
// 5.9 and not enough to invite arithmetic, and a size that changed its
// number of decimals with its magnitude would make two lines of one list
// impossible to compare down the column.
//
// BELOW A KILOBYTE THE EXACT COUNT IS PRINTED, because at that size the
// exact count is shorter than the rounded one and is the more useful
// fact: "412 B" beats "0.4 kB".
//
// IT STEPS UP WHEN ROUNDING WOULD OTHERWISE PRODUCE A FULL THOUSAND OF
// THE SMALLER UNIT. 999,999 bytes divided by a thousand is 999.999,
// which renders as "1000.0 kB" — a spelling no reader expects and one
// that sorts wrongly beside "1.0 MB" in the same list. The decision is
// made on the ROUNDED value rather than the raw one, because it is the
// rounded value that gets printed.
func Bytes(n int64) string {
	if n > -1000 && n < 1000 {
		return strconv.FormatInt(n, 10) + " B"
	}

	sign, size := "", float64(n)
	if size < 0 {
		sign, size = "-", -size
	}

	scale := 1000.0
	for i := range prefixes {
		scaled := size / scale
		if i == len(prefixes)-1 || math.Round(scaled*10)/10 < 1000 {
			return fmt.Sprintf("%s%.1f %s", sign, scaled, prefixes[i])
		}
		scale *= 1000
	}

	// Unreachable: the loop returns on its last iteration whatever the
	// value. Written out rather than left to the compiler because a
	// reader should not have to prove that to themselves.
	panic("units: the prefix list is empty")
}

// Count renders a count with thousands separators: "3,412".
//
// IT IS HERE RATHER THAN BESIDE ITS CALLER because it is half of the
// same sentence Bytes writes the other half of — "3,412 files" and
// "18.9 MB" appear in one message and in one column — and a program
// whose two number formats live in two packages is a program that will
// eventually group one of them and not the other.
//
// The separator is a comma and is not negotiable per locale. This
// program has one language, and a formatter that guessed at the reader's
// would produce a different message on two machines running one deploy.
func Count(n int) string {
	digits := strconv.Itoa(n)
	sign := ""
	if strings.HasPrefix(digits, "-") {
		sign, digits = "-", digits[1:]
	}

	var b strings.Builder
	b.WriteString(sign)
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}
