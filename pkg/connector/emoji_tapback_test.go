package connector

import "testing"

func TestEmojiToTapbackTypeIgnoresVariationSelectors(t *testing.T) {
	cases := map[string]uint32{
		"👍": 1, "👍️": 1,
		"❤": 0, "❤️": 0, "♥️": 0,
		"‼": 4, "‼️": 4,
		"👎️": 2,
	}
	for in, want := range cases {
		got, custom := emojiToTapbackType(in)
		if got != want || custom != nil {
			t.Errorf("emojiToTapbackType(%q) = %d, %v; want %d, nil", in, got, custom, want)
		}
	}
	if got, custom := emojiToTapbackType("👋🏽"); got != 6 || custom == nil || *custom != "👋🏽" {
		t.Errorf("non-classic emoji should keep its skin tone as a custom tapback, got %d", got)
	}
}
