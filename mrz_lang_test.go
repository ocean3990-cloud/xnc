package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindMRZLangIn(t *testing.T) {
	// No model anywhere → generic English, no custom tessdata dir.
	empty := t.TempDir()
	if l, d, ok := findMRZLangIn([]string{empty}); ok || l != "eng" || d != "" {
		t.Fatalf("empty dir: got (%q,%q,%v), want (eng,\"\",false)", l, d, ok)
	}

	// Drop an mrz.traineddata and it is picked up with the dir returned so the
	// caller can pass --tessdata-dir.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mrz.traineddata"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	l, d, ok := findMRZLangIn([]string{empty, dir})
	if !ok || l != "mrz" || d != dir {
		t.Fatalf("mrz model: got (%q,%q,%v), want (mrz,%q,true)", l, d, ok, dir)
	}

	// ocrb.traineddata is also accepted.
	dir2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir2, "ocrb.traineddata"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if l, _, ok := findMRZLangIn([]string{dir2}); !ok || l != "ocrb" {
		t.Fatalf("ocrb model: got lang %q ok %v, want ocrb/true", l, ok)
	}
}
