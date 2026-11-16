package domain

import "testing"

func TestValidateIconValue(t *testing.T) {
	ok := []string{"", "🚀", "❤️", "👍🏽", "🇷🇺", "1️⃣", "👨‍👩‍👧‍👦", "🏳️‍🌈", "⭐", "☀️", "✅", "✈️", "☎", "🀄", "🫩", "icon:rocket", "icon:a", "icon:folder-kanban", "icon:x1-2"}
	bad := []string{
		"icon:Rocket!", "icon:Rocket", "icon:", "icon:with space", "icon:under_score", "<svg>", "<svg onload=x>",
		"🚀🚀", "🚀a", "a🚀", "ab", "rocket", " 🚀", "🇷", "1", "🏽", "👨‍", "🚀‍",
		"icon:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", // 41 chars
		"icon:🚀", "icon:rocket🚀", "ICON:rocket",
		// symbols from emoji blocks that are not emoji
		"\u2630", "\u2630\uFE0F", "\u2619", "\u27B0\u27B1", "\u2B58", "\U0001F000", "\U0001F650", "\U0001F700", "\u00B6",
	}
	for _, s := range ok {
		if err := ValidateIconValue(s); err != nil {
			t.Errorf("%q should be accepted: %v", s, err)
		}
	}
	for _, s := range bad {
		if err := ValidateIconValue(s); err == nil {
			t.Errorf("%q should be rejected", s)
		}
	}
}

func TestEmojiBasesSortedDisjoint(t *testing.T) {
	for i, r := range emojiBases {
		if r[0] > r[1] || (i > 0 && emojiBases[i-1][1] >= r[0]) {
			t.Fatalf("emojiBases[%d] = %X..%X breaks the order", i, r[0], r[1])
		}
	}
	for _, r := range []rune{0x2630, 0x2605, 0x27B1, 0x1F1E6, 0x1F3FB, '#', '1', 0x1F000, 0x1F650} {
		if isPictographic(r) {
			t.Errorf("U+%04X must not be an emoji base", r)
		}
	}
	for _, r := range []rune{0xA9, 0x2600, 0x2705, 0x27BF, 0x1F004, 0x1F680, 0x1FAF8} {
		if !isPictographic(r) {
			t.Errorf("U+%04X must be an emoji base", r)
		}
	}
}
