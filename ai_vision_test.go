package main

import (
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractJSON(t *testing.T) {
	cases := map[string]string{
		`{"a":1}`:                            `{"a":1}`,
		"```json\n{\"a\":1}\n```":            `{"a":1}`,
		"here you go:\n{\"a\":1}\nthank you": `{"a":1}`,
		"```\n{\"nested\":{\"b\":2}}\n```":   `{"nested":{"b":2}}`,
	}
	for in, want := range cases {
		if got := extractJSON(in); got != want {
			t.Fatalf("extractJSON(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDownscaleImage(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4400, 2200))
	out := downscaleImage(src, maxAIEdge)
	if out.Bounds().Dx() != maxAIEdge {
		t.Fatalf("long edge not clamped: %dx%d", out.Bounds().Dx(), out.Bounds().Dy())
	}
	// An image already within bounds is returned untouched.
	small := image.NewRGBA(image.Rect(0, 0, 100, 80))
	if downscaleImage(small, maxAIEdge) != image.Image(small) {
		t.Fatal("small image should be returned unchanged")
	}
}

// writeTestPNG creates a tiny decodable PNG so imageForAI has real bytes to work
// with (the pixel content is irrelevant — the mock server ignores the image).
func writeTestPNG(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := 0; i < 8; i++ {
		img.SetRGBA(i, i, color.RGBA{0, 0, 0, 255})
	}
	p := filepath.Join(t.TempDir(), "p.png")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestExtractPassportAIMRZCorrection drives the full AI passport path against a
// mock Messages endpoint: the model returns a passport number with one wrong
// digit, but a valid MRZ, and the checksum cross-check must repair it.
func TestExtractPassportAIMRZCorrection(t *testing.T) {
	l1 := "P<USATRAN<<DIEP<THI<HOANG<<<<<<<<<<<<<<<<<<<"
	l2 := "A691252571USA7909087F3506053356549008<676742"
	reply := map[string]string{
		"fullName":    "TRAN DIEP THI HOANG",
		"birthDate":   "08/09/1979",
		"gender":      "F",
		"nationality": "USA",
		"passport":    "A69125999", // deliberately wrong tail
		"mrzLine1":    l1,
		"mrzLine2":    l2,
	}
	replyJSON, _ := json.Marshal(reply)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") == "" {
			t.Error("missing x-api-key header")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"stop_reason": "end_turn",
			"content":     []map[string]string{{"type": "text", "text": string(replyJSON)}},
		})
	}))
	defer srv.Close()

	old := aiEndpoint
	aiEndpoint = srv.URL
	defer func() { aiEndpoint = old }()
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	g, _, err := extractPassportAI(writeTestPNG(t))
	if err != nil {
		t.Fatalf("extractPassportAI: %v", err)
	}
	if g.Passport != "A69125257" {
		t.Fatalf("MRZ checksum did not correct passport: got %q, want A69125257", g.Passport)
	}
	if g.FullName != "TRAN DIEP THI HOANG" || g.BirthDate != "08/09/1979" || g.Gender != "F" || g.Nationality != "USA" {
		t.Fatalf("unexpected guest: %#v", g)
	}
	if g.BirthPrecision != "D" {
		t.Fatalf("birth precision = %q, want D", g.BirthPrecision)
	}
}

// TestExtractTableAI drives the multi-guest table path against a mock endpoint.
func TestExtractTableAI(t *testing.T) {
	reply := map[string]any{"guests": []map[string]string{
		{"fullName": "LEE KUO TSENG", "birthDate": "23/12/1965", "gender": "M", "nationality": "TWN", "passport": "365751437", "room": "01"},
		{"fullName": "LIU HUI CHEN", "birthDate": "11/04/1970", "gender": "F", "nationality": "TWN", "passport": "351493486", "room": "02"},
	}}
	replyJSON, _ := json.Marshal(reply)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"stop_reason": "end_turn",
			"content":     []map[string]string{{"type": "text", "text": string(replyJSON)}},
		})
	}))
	defer srv.Close()
	old := aiEndpoint
	aiEndpoint = srv.URL
	defer func() { aiEndpoint = old }()
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	recs, _, err := extractTableAI(writeTestPNG(t))
	if err != nil {
		t.Fatalf("extractTableAI: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d guests, want 2: %#v", len(recs), recs)
	}
	if recs[0].Passport != "365751437" || recs[0].Gender != "M" || recs[1].FullName != "LIU HUI CHEN" {
		t.Fatalf("unexpected guests: %#v", recs)
	}
}
