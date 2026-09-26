package course

import "testing"

func TestSanitize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Café naïve", "Cafe naive"},
		{"Ⅰ. Intro", "I. Intro"},                       // Roman numeral: NFKD compat-decomposes to "I" before TRANSLIT ever sees U+2160 (see names.go doc comment)
		{"“quoted”", "quoted"},                         // curly double quotes dropped
		{"it’s", "it's"},                               // curly apostrophe kept as '
		{"a/b\\c", "a-b-c"},                            // path separators
		{"ratio:", "ratio -"},                          // colon -> " - ", trailing space then stripped by strip(" .")
		{"dots...collapse", "dots.collapse"},           // consecutive dots collapse
		{"dots . . . collapse", "dots . . . collapse"}, // NOT collapsed: dots aren't adjacent, regex is `\.{2,}`
		{"CON", "_CON"},                                // Windows reserved
		{"conf", "conf"},                               // ...only as the whole stem
		{"CON.notes", "_CON.notes"},
		{"trailing . ", "trailing"},
		{"<>\"|?*", "untitled"}, // every char dropped
		{"   ", "untitled"},
		{"über…", "uber"}, // ellipsis dropped
	}
	for _, c := range cases {
		if got := Sanitize(c.in); got != c.want {
			t.Errorf("Sanitize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSanitizeLimitTruncatesAndStrips(t *testing.T) {
	got := SanitizeLimit("abcdefghij", 6)
	if got != "abcdef" {
		t.Fatalf("got %q", got)
	}
	if got := SanitizeLimit("abc ", 4); got != "abc" {
		t.Fatalf("trailing space not stripped: %q", got)
	}
}

func TestUniqueNameSuffixes(t *testing.T) {
	used := map[string]bool{}
	if got := UniqueName("hero", ".png", used); got != "hero.png" {
		t.Fatalf("got %q", got)
	}
	if got := UniqueName("hero", ".png", used); got != "hero-2.png" {
		t.Fatalf("collision not suffixed: %q", got)
	}
	if got := UniqueName("hero", ".png", used); got != "hero-3.png" {
		t.Fatalf("got %q", got)
	}
}

func TestSubtitleBasename(t *testing.T) {
	if got := SubtitleBasename("Title", "EN", "", 0); got != "Title.en.vtt" {
		t.Fatalf("got %q", got)
	}
	if got := SubtitleBasename("Title", "EN", "", 2); got != "Title.en.2.vtt" {
		t.Fatalf("got %q", got)
	}
	if got := SubtitleBasename("Title", "", "Español", 0); got != "Title.espanol.vtt" {
		t.Fatalf("label fallback: got %q", got)
	}
	if got := SubtitleBasename("Title", "", "", 0); got != "Title.sub.vtt" {
		t.Fatalf("default: got %q", got)
	}
}

func TestSubtitleLangKey(t *testing.T) {
	if got := SubtitleLangKey("EN", ""); got != "en" {
		t.Fatalf("got %q", got)
	}
	if got := SubtitleLangKey("", "Español"); got != "español" {
		t.Fatalf("label fallback: got %q", got)
	}
	if got := SubtitleLangKey("", ""); got != "sub" {
		t.Fatalf("default: got %q", got)
	}
}
