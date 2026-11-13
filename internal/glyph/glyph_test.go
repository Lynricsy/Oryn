package glyph

import "testing"

func TestSingle(t *testing.T) {
	ok := []string{"🧪", "\U000F0668", "\uf121", "⏳", "🗄️", "👍🏽", "👩‍💻", "🇨🇳", "🏴󠁧󠁢󠁳󠁣󠁴󠁿", "#️⃣", "A", "é"}
	for _, s := range ok {
		if !Single(s, 32) {
			t.Errorf("Single(%q) = false，应为 true", s)
		}
	}
	bad := []string{"", " ", "ab", "🧪🧪", "🧪 ", "\n", "\u200D", "👩‍", "🇨🇳🇨", "\uFE0F", string([]byte{0xff})}
	for _, s := range bad {
		if Single(s, 32) {
			t.Errorf("Single(%q) = true，应为 false", s)
		}
	}
	if Single("👩‍💻", 4) {
		t.Error("超过字节上限仍通过")
	}
}

func TestParseTitleCard(t *testing.T) {
	cases := []struct {
		in   string
		want TitleCard
		ok   bool
	}{
		{"🧪 FLAKY: 修复不稳定的重连测试", TitleCard{"🧪", "FLAKY", "修复不稳定的重连测试"}, true},
		{"\U000F0668 WS: Rework reconnect", TitleCard{"\U000F0668", "WS", "Rework reconnect"}, true},
		{"🗄️ DB2: Migrate: schema", TitleCard{"🗄️", "DB2", "Migrate: schema"}, true},
		{"普通标题", TitleCard{}, false},
		{"🧪 flaky: lower case code", TitleCard{}, false},
		{"🧪 TOOLONG7: x", TitleCard{}, false},
		{"x FIX: ascii icon", TitleCard{}, false},
		{"🧪🧪 FIX: two icons", TitleCard{}, false},
		{"🧪 FIX:  leading space", TitleCard{}, false},
		{"🧪 FIX:", TitleCard{}, false},
	}
	for _, c := range cases {
		got, ok := ParseTitleCard(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("ParseTitleCard(%q) = %+v, %v；应为 %+v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}
