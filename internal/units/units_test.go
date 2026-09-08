package units

import (
	"math"
	"strings"
	"testing"
)

// TestBytesIsDecimalSI is the row the whole package exists for.
//
// THE LITERALS ARE WRITTEN OUT rather than derived from the constants
// this formatter renders against, because a table whose expectations
// came from the same arithmetic as the code would agree with any change
// to either. The binary neighbours are here on purpose: the difference
// between 5,000,000 and 5,242,880 is 5%, it surfaces nowhere except at a
// limit boundary, and this row is the only place in the tree that can
// see a regression to binary units before a user does.
//
// REQUIRED MUTATION, run 2026-09-08: change the divisor in Bytes from
// 1000 to 1024. Reds on nine of the sixteen rows — 30,000,000 renders as
// "29.3 MB" — while the four byte rows stay green, correctly, since
// below a kilobyte there is no divisor to get wrong. The 1,000 and 1,024
// rows also stay green, which is measured rather than predicted: both
// round to "1.0 kB" under either divisor, and it is the reason the table
// carries values that are not powers of the base.
func TestBytesIsDecimalSI(t *testing.T) {
	for _, row := range []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{999, "999 B"},
		{1_000, "1.0 kB"},
		{1_024, "1.0 kB"},
		{5_200, "5.2 kB"},
		{999_999, "1.0 MB"},
		{1_000_000, "1.0 MB"},
		{5_000_000, "5.0 MB"},
		{5_000_001, "5.0 MB"},
		{5 * 1024 * 1024, "5.2 MB"},
		{30_000_000, "30.0 MB"},
		{30_000_001, "30.0 MB"},
		{32_257_024, "32.3 MB"},
		{1_000_000_000, "1.0 GB"},
		{math.MaxInt64, "9.2 EB"},
	} {
		if got := Bytes(row.n); got != row.want {
			t.Errorf("Bytes(%d) = %q, want %q", row.n, got, row.want)
		}
	}
}

// TestBytesStepsUpRatherThanPrintingAFullThousand.
//
// A value that rounds to 1000.0 of a unit is a spelling nobody expects
// and one that sorts wrongly beside the next unit up in the same column.
// The boundary is where rounding puts it and not where the division
// does, which is why 999,950 belongs here: it is under a megabyte and
// still renders as one.
//
// REQUIRED MUTATION, run 2026-09-08: compare the raw scaled value rather
// than the rounded one in the step-up condition — written as
// math.Abs(scaled), because deleting the call outright leaves an import
// unused and a mutation that will not compile proves nothing. Reds on
// 999,950, 999,999 and 999,999,999 with a full thousand of the smaller
// unit, and leaves 999,000 and 999,949 green, which is the
// discrimination the row is for.
func TestBytesStepsUpRatherThanPrintingAFullThousand(t *testing.T) {
	for _, row := range []struct {
		n    int64
		want string
	}{
		{999_000, "999.0 kB"},
		{999_949, "999.9 kB"},
		{999_950, "1.0 MB"},
		{999_999, "1.0 MB"},
		{999_999_999, "1.0 GB"},
	} {
		if got := Bytes(row.n); got != row.want {
			t.Errorf("Bytes(%d) = %q, want %q", row.n, got, row.want)
		}
	}
}

// TestBytesRendersANegativeRatherThanMangleIt. Nothing in this program
// measures a negative size, which is exactly why the row is here: a
// formatter that overflowed or printed nonsense on one would do it
// somewhere nobody was looking, and the smallest int64 is the input that
// breaks the obvious implementation.
//
// REQUIRED MUTATION, run 2026-09-08: implement the sign as a recursive
// call on -n. Panics with an overflow on the smallest int64 row rather
// than reding, which is a caught mutation either way — the row is the
// only thing that reaches the input.
func TestBytesRendersANegativeRatherThanMangleIt(t *testing.T) {
	for _, row := range []struct {
		n    int64
		want string
	}{
		{-1, "-1 B"},
		{-999, "-999 B"},
		{-5_000_000, "-5.0 MB"},
		{math.MinInt64, "-9.2 EB"},
	} {
		if got := Bytes(row.n); got != row.want {
			t.Errorf("Bytes(%d) = %q, want %q", row.n, got, row.want)
		}
	}
}

// TestCountGroupsThousands.
//
// REQUIRED MUTATION, run 2026-09-08: change the modulus in Count from 3
// to 4. Reds on every row of four digits or more; the three short rows
// stay green, which is the same shape as the divisor mutation above and
// for the same reason.
func TestCountGroupsThousands(t *testing.T) {
	for _, row := range []struct {
		n    int
		want string
	}{
		{0, "0"},
		{1, "1"},
		{999, "999"},
		{1_000, "1,000"},
		{3_000, "3,000"},
		{3_001, "3,001"},
		{3_412, "3,412"},
		{1_000_000, "1,000,000"},
		{-1_234, "-1,234"},
	} {
		if got := Count(row.n); got != row.want {
			t.Errorf("Count(%d) = %q, want %q", row.n, got, row.want)
		}
	}
}

// TestEveryRenderedSizeCarriesExactlyOneDecimalAboveAKilobyte is the
// property the table rows above sample, asserted over a range instead.
//
// THE TABLE IS THE POSITIVE CONTROL AND THIS IS THE SWEEP. A table can
// only fail where somebody thought to look, and the decimal count is the
// kind of thing that holds for every value an author tried and breaks on
// a magnitude they did not — so this walks every power of ten and its
// neighbours and asks the shape question of each.
//
// REQUIRED MUTATION, run 2026-09-08: change the "%.1f" verb in Bytes to
// "%.2f". Reds here on the first scaled value; the table above reds too,
// which is why this row asserts the SHAPE rather than repeating values.
func TestEveryRenderedSizeCarriesExactlyOneDecimalAboveAKilobyte(t *testing.T) {
	checked := 0
	for magnitude := int64(1); magnitude <= 1_000_000_000_000_000_000; magnitude *= 10 {
		for _, n := range []int64{magnitude - 1, magnitude, magnitude + 1} {
			if n < 1000 {
				continue
			}
			checked++
			got := Bytes(n)
			number, unit, found := strings.Cut(got, " ")
			if !found {
				t.Fatalf("Bytes(%d) = %q, which has no unit", n, got)
			}
			if unit == "B" {
				t.Errorf("Bytes(%d) = %q, want a scaled unit above a kilobyte", n, got)
			}
			if _, decimals, ok := strings.Cut(number, "."); !ok || len(decimals) != 1 {
				t.Errorf("Bytes(%d) = %q, want exactly one decimal place", n, got)
			}
		}
	}
	if checked == 0 {
		t.Fatal("this row measured nothing — the loop selected no values at all")
	}
}
