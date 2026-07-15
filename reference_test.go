package main

import "testing"

func TestNationalityCodes(t *testing.T) {
	m := nationalityCodes()
	if len(m) < 150 {
		t.Fatalf("expected a full country list, got %d entries", len(m))
	}
	for code, want := range map[string]string{
		"USA": "United States of America",
		"VNM": "Viet Nam",
		"TWN": "Taiwan",
	} {
		if m[code] != want {
			t.Fatalf("code %s = %q, want %q", code, m[code], want)
		}
	}
}
