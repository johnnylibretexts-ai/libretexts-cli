package main

import (
	"errors"
	"math"
	"reflect"
	"testing"
)

func TestSelectContentWindowsUnicodeByCodePoint(t *testing.T) {
	got, window, err := selectContent("A界🙂B", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got != "界🙂" {
		t.Fatalf("content = %q, want %q", got, "界🙂")
	}
	want := contentWindow{Offset: 1, ReturnedChars: 2, TotalChars: 4, Truncated: true, NextOffset: intPointer(3)}
	if !reflect.DeepEqual(window, want) {
		t.Fatalf("window = %+v, want %+v", window, want)
	}
}

func TestSelectContentRejectsOffsetPastEnd(t *testing.T) {
	_, _, err := selectContent("abc", 4, 1)
	var coded *agentError
	if !errors.As(err, &coded) || coded.Code != "INVALID_ARGUMENT" {
		t.Fatalf("error = %v, want INVALID_ARGUMENT", err)
	}
}

func TestSelectContentBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		offset    int
		maxChars  int
		wantValue string
		wantError bool
	}{
		{"empty", "", 0, 5, "", false},
		{"exact end", "abc", 3, 5, "", false},
		{"unlimited remainder", "abcd", 1, 0, "bcd", false},
		{"negative offset", "abc", -1, 1, "", true},
		{"negative limit", "abc", 0, -1, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := selectContent(tt.value, tt.offset, tt.maxChars)
			if (err != nil) != tt.wantError || got != tt.wantValue {
				t.Fatalf("selectContent(%q, %d, %d) = %q, %v", tt.value, tt.offset, tt.maxChars, got, err)
			}
		})
	}
}

func TestSelectContentHandlesMaxIntWithoutOverflow(t *testing.T) {
	got, window, err := selectContent("abcd", 1, math.MaxInt)
	if err != nil {
		t.Fatal(err)
	}
	if got != "bcd" {
		t.Fatalf("content = %q, want %q", got, "bcd")
	}
	want := contentWindow{Offset: 1, ReturnedChars: 3, TotalChars: 4}
	if !reflect.DeepEqual(window, want) {
		t.Fatalf("window = %+v, want %+v", window, want)
	}
}
