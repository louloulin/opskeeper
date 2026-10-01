// This file pins the token accounting contract, which is the one part of
// ports that a provider implementation can silently get wrong in a way no
// compiler notices: every field is an int, and a wrong total is a plausible
// number rather than a compile error.
package ports

import "testing"

// The fallback is the sum, so a provider that reports no total at all still
// produces a usable number rather than a zero that reads as "free".
func TestUsageTotalFallsBackToTheSum(t *testing.T) {
	t.Parallel()
	u := Usage{
		InputTokens:      11,
		OutputTokens:     4,
		CacheReadTokens:  2,
		CacheWriteTokens: 3,
	}
	if got := u.Total(); got != 20 {
		t.Errorf("Total() = %d, want 20 (11+4+2+3)", got)
	}
}

// A provider that reports its own total is believed, even when the sum
// disagrees. Reasoning models bill reasoning tokens that appear in neither
// input nor output, so recomputing would under-report the bill — and the bill
// is the one number in this struct a caller must not second-guess.
func TestUsageTotalPrefersTheProviderReportedTotal(t *testing.T) {
	t.Parallel()
	// 11 + 4 = 15 by the sum, but the provider billed 15 while naming
	// reasoning tokens in neither field: the exact shape that makes the
	// sum a lower bound rather than an estimate.
	u := Usage{InputTokens: 11, OutputTokens: 4, ReportedTotal: 15}
	if got := u.Total(); got != 15 {
		t.Errorf("Total() = %d, want the reported 15", got)
	}
}

// A reported total below the sum is still believed. A provider that
// discounts cached input would otherwise be silently over-charged by the
// fallback, and "trust the provider's arithmetic" has to mean it in both
// directions or it is not a rule.
func TestUsageTotalBelievesALowerReportedTotal(t *testing.T) {
	t.Parallel()
	u := Usage{InputTokens: 100, OutputTokens: 5, CacheReadTokens: 90, ReportedTotal: 105}
	if got := u.Total(); got != 105 {
		t.Errorf("Total() = %d, want the reported 105 rather than the sum 195", got)
	}
}

// The zero case is what makes the fallback reachable at all: a provider that
// omits usage entirely must not read as an infinitely cheap request.
func TestAnEmptyUsageIsZero(t *testing.T) {
	t.Parallel()
	if got := (Usage{}).Total(); got != 0 {
		t.Errorf("Total() = %d, want 0", got)
	}
}
