package main

import (
	"image"
	"image/color"
	"testing"
)

func TestPreferCompleteName(t *testing.T) {
	cases := []struct{ primary, alt, want string }{
		{"HOANG", "TRAN DIEP THI HOANG", "TRAN DIEP THI HOANG"},  // alt more complete
		{"TRAN DIEP THI HOANG", "HOANG", "TRAN DIEP THI HOANG"},  // primary more complete
		{"PHAM AMY VI", "", "PHAM AMY VI"},                       // alt empty
		{"", "NGUYEN VAN A", "NGUYEN VAN A"},                     // primary empty
		{"PHAMK KAMY VI KKKKRRRR", "PHAM AMY VI", "PHAM AMY VI"}, // garbled primary → alt
		{"LEE MING", "WONG TAI", "LEE MING"},                     // tie on token count → primary
		{"", "", ""},                                             // both empty
	}
	for _, c := range cases {
		if got := preferCompleteName(c.primary, c.alt); got != c.want {
			t.Fatalf("preferCompleteName(%q,%q)=%q want %q", c.primary, c.alt, got, c.want)
		}
	}
}

func TestVisualNameRecoversGivenFromLabels(t *testing.T) {
	// MRZ line 1 truncated so only the surname survives; the printed labels still
	// carry the full given names. parsePassportVisual must return the complete name
	// (this is the "NGUYEN" → "NGUYEN TRINITY HOANG" case).
	txt := "P<USANGUYEN<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<\n" +
		"6831017213USA0809214F2707266425711567<991344\n" +
		"SURNAME/NOM/APELLIDOS\nNGUYEN\n" +
		"GIVEN NAMES/PRENOMS/NOMBRES\nTRINITY HOANG\n" +
		"SEX/SEXE/SEXO\nF\n"
	g := parsePassportVisual(txt)
	if g.FullName != "NGUYEN TRINITY HOANG" {
		t.Fatalf("name = %q, want NGUYEN TRINITY HOANG", g.FullName)
	}
}

func TestMRZNationalityCrossCheck(t *testing.T) {
	l1 := "P<USATRAN<<DIEP<THI<HOANG<<<<<<<<<<<<<<<<<<<" // issuer USA
	// nationality field (chars 11-13) says AUS — a plausible USA↔AUS misread.
	mismatch := guestFromMRZLines(l1, "A691252571AUS7909087F3506053356549008<676742")
	if mismatch.Nationality != "AUS" || mismatch.NatCross != "USA" {
		t.Fatalf("expected nationality AUS with cross USA, got %q / %q", mismatch.Nationality, mismatch.NatCross)
	}
	// When issuer and nationality agree, no cross-check flag is raised.
	ok := guestFromMRZLines(l1, "A691252571USA7909087F3506053356549008<676742")
	if ok.NatCross != "" {
		t.Fatalf("expected no cross flag, got %q", ok.NatCross)
	}
}

func TestSharpenGray(t *testing.T) {
	// Uniform image: 3x3 box blur equals the pixel, so unsharp is a no-op.
	uni := image.NewGray(image.Rect(0, 0, 5, 5))
	for y := 0; y < 5; y++ {
		for x := 0; x < 5; x++ {
			uni.SetGray(x, y, color.Gray{Y: 120})
		}
	}
	so := sharpenGray(uni)
	if so.Bounds() != uni.Bounds() {
		t.Fatalf("size changed: %v", so.Bounds())
	}
	if so.GrayAt(2, 2).Y != 120 {
		t.Fatalf("uniform pixel changed: %d", so.GrayAt(2, 2).Y)
	}

	// A bright center on a darker field must be pushed brighter (edge enhanced).
	g := image.NewGray(image.Rect(0, 0, 3, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 3; x++ {
			g.SetGray(x, y, color.Gray{Y: 100})
		}
	}
	g.SetGray(1, 1, color.Gray{Y: 200})
	if got := sharpenGray(g).GrayAt(1, 1).Y; got <= 200 {
		t.Fatalf("center not sharpened: %d (want > 200)", got)
	}
}
