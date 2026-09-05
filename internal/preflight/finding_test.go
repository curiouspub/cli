package preflight

import "testing"

// TestFindingRoundTrips is the shape test: a Finding constructed with
// either severity carries its check id, severity and message back out
// unchanged, and both severities construct.
func TestFindingRoundTrips(t *testing.T) {
	cases := []struct {
		name     string
		severity Severity
	}{
		{"hard stop", SeverityHardStop},
		{"warning", SeverityWarning},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := Finding{
				CheckID:  "astro-in-package-json",
				Severity: tc.severity,
				Message:  "this does not look like an Astro project",
			}
			if f.CheckID != "astro-in-package-json" {
				t.Errorf("CheckID = %q, want %q", f.CheckID, "astro-in-package-json")
			}
			if f.Severity != tc.severity {
				t.Errorf("Severity = %q, want %q", f.Severity, tc.severity)
			}
			if f.Message != "this does not look like an Astro project" {
				t.Errorf("Message = %q, want %q", f.Message, "this does not look like an Astro project")
			}
		})
	}
}
