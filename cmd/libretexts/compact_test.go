package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCompactCountsRunesNotBytes(t *testing.T) {
	// 200 accented characters is 400 bytes, so a byte-length check truncates a
	// string that is well under the character limit.
	s := strings.Repeat("é", 200)
	got := compact(s, 220)
	if got != s {
		t.Fatalf("compact truncated a %d-character string at a limit of 220", len([]rune(s)))
	}
}

func TestCompactNeverSplitsACodepoint(t *testing.T) {
	s := strings.Repeat("é", 400)
	got := compact(s, 220)
	if !utf8.ValidString(got) {
		t.Fatalf("compact produced invalid UTF-8: %q", got[len(got)-8:])
	}
	if runes := []rune(got); len(runes) != 220 {
		t.Fatalf("compact returned %d characters, want 220", len(runes))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("truncated output does not end in an ellipsis: %q", got[len(got)-8:])
	}
}

func TestCompactSmallAndZeroLimits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		n     int
		want  string
	}{
		{name: "zero", input: "hello", n: 0, want: ""},
		{name: "negative", input: "hello", n: -1, want: ""},
		{name: "shorter than the ellipsis", input: "hello", n: 2, want: "he"},
		{name: "exactly the ellipsis", input: "hello", n: 3, want: "hel"},
		{name: "one past the ellipsis", input: "hello", n: 4, want: "h..."},
		{name: "fits", input: "hello", n: 5, want: "hello"},
		{name: "collapses whitespace", input: "  a\t\n b  ", n: 10, want: "a b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Previously n < 3 panicked with a negative slice bound.
			if got := compact(tc.input, tc.n); got != tc.want {
				t.Fatalf("compact(%q, %d) = %q, want %q", tc.input, tc.n, got, tc.want)
			}
		})
	}
}
