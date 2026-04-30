package main

import "testing"

func TestIsEmojiOnly(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"single emoji", "😀", true},
		{"multiple emoji", "😀🎉🚀", true},
		{"compound ZWJ family", "👨‍👩‍👧", true},
		{"skin tone modifier", "👍🏽", true},
		{"flag sequence", "🇰🇷", true},
		{"heart (older codepoint)", "❤️", true},
		{"keycap digit", "1️⃣", true},
		{"keycap hash", "#️⃣", true},
		{"empty string", "", false},
		{"whitespace only", "   ", false},
		{"single ascii letter", "a", false},
		{"emoji with text", "hi 😀", false},
		{"text with emoji", "😀 hi", false},
		{"hangul", "안녕", false},
		{"plain digit no keycap", "1", true},
		{"mixed emoji and ascii", "😀a", false},
		{"trailing ascii", "🚀!", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isEmojiOnly(tc.in); got != tc.want {
				t.Fatalf("isEmojiOnly(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
