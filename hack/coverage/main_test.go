package main

import (
	"os"
	"strings"
	"testing"
)

func TestParseProfileCountsStatementsAndUncoveredBlocks(t *testing.T) {
	profile, err := os.CreateTemp(t.TempDir(), "coverage-*.out")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := profile.WriteString("mode: set\nsource.go:1.1,2.2 3 1\nsource.go:3.1,4.2 2 0\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := profile.Seek(0, 0); err != nil {
		t.Fatal(err)
	}

	covered, total, err := parseProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if covered != 3 || total != 5 {
		t.Fatalf("parseProfile() = (%d, %d), want (3, 5)", covered, total)
	}
}

func TestParseProfileRejectsMalformedRows(t *testing.T) {
	profile, err := os.CreateTemp(t.TempDir(), "coverage-*.out")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := profile.WriteString("mode: set\nsource.go:1.1,2.2 nope 1\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := profile.Seek(0, 0); err != nil {
		t.Fatal(err)
	}

	if _, _, err := parseProfile(profile); err == nil || !strings.Contains(err.Error(), "invalid statement count") {
		t.Fatalf("parseProfile() error = %v, want invalid statement count", err)
	}
}
