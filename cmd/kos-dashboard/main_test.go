package main

import "testing"

// TestFrameOriginRepeats checks that every -frame-origin the operator names is
// kept: the flag is documented as repeatable and the front frames each origin
// (KOSD-F-18).
func TestFrameOriginRepeats(t *testing.T) {
	parsed, err := parseOptions([]string{
		"-frame-origin", "https://a.example.test",
		"-frame-origin=https://b.example.test",
	})
	if err != nil {
		t.Fatalf("parse the options: %v", err)
	}
	if len(parsed.frameOrigins) != 2 ||
		parsed.frameOrigins[0] != "https://a.example.test" ||
		parsed.frameOrigins[1] != "https://b.example.test" {
		t.Fatalf("frame origins = %v, want both named origins", parsed.frameOrigins)
	}
}

// TestOptionsFrameNothingByDefault checks the front frames only the origins an
// operator named (KOSD-F-18).
func TestOptionsFrameNothingByDefault(t *testing.T) {
	parsed, err := parseOptions(nil)
	if err != nil {
		t.Fatalf("parse the options: %v", err)
	}
	if len(parsed.frameOrigins) != 0 {
		t.Fatalf("frame origins = %v, want none", parsed.frameOrigins)
	}
	if parsed.address != ":8080" || parsed.upstream != "http://127.0.0.1:8081" || parsed.secure {
		t.Fatalf("options = %+v, want the documented defaults", parsed)
	}
}

// TestFrameOriginRefusesAnEmptyOrigin checks the flag cannot silently admit a
// frameless entry an operator typed by mistake.
func TestFrameOriginRefusesAnEmptyOrigin(t *testing.T) {
	if _, err := parseOptions([]string{"-frame-origin", ""}); err == nil {
		t.Fatal("an empty -frame-origin is accepted")
	}
}
