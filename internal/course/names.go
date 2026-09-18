// Package course sanitises FutureLearn titles into filesystem-safe names.
package course

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// translit holds explicit char -> replacement pairs for things NFKD won't
// fully handle.
//
// The Roman numeral entries (U+2160-U+2169) are unreachable in practice:
// NFKD gives those code points a compatibility decomposition to plain Latin
// letters (U+2160 -> "I", U+2161 -> "II", ...), which runs before this table
// is consulted. Kept anyway in case that decomposition ever changes.
var translit = map[rune]string{
	'‘': "'", '’': "'", '‚': "'", '‛': "'", // curly apostrophes
	'“': "", '”': "", '„': "", '‟': "", // curly double quotes
	'–': "-", '—': "-", '―': "-", // dashes
	'…': "",  // ellipsis
	' ': " ", // non-breaking space
	':': " - ",
	'/': "-", '\\': "-", // path separators -> hyphen
	'Ⅰ': "1", 'Ⅱ': "2", 'Ⅲ': "3", 'Ⅳ': "4", // Roman numerals
	'Ⅴ': "5", 'Ⅵ': "6", 'Ⅶ': "7", 'Ⅷ': "8",
	'Ⅸ': "9", 'Ⅹ': "10",
}

// drop holds characters illegal on Windows or otherwise problematic in a filename.
var drop = map[rune]bool{'<': true, '>': true, '"': true, '|': true, '?': true, '*': true}

var windowsReserved = buildWindowsReserved()

func buildWindowsReserved() map[string]bool {
	m := map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true}
	for i := 1; i <= 9; i++ {
		m[fmt.Sprintf("COM%d", i)] = true
		m[fmt.Sprintf("LPT%d", i)] = true
	}
	return m
}

var (
	whitespaceRun = regexp.MustCompile(`\s+`)
	dotRun        = regexp.MustCompile(`\.{2,}`)
)

// Sanitize turns name into a filesystem-safe path component, capped at 120 runes.
func Sanitize(name string) string {
	return SanitizeLimit(name, 120)
}

// SanitizeLimit turns name into a filesystem-safe path component, capped at maxLen runes.
func SanitizeLimit(name string, maxLen int) string {
	name = html.UnescapeString(name)
	name = norm.NFKD.String(name) // ü -> u, ligatures -> letters

	var b strings.Builder
	for _, r := range name {
		if repl, ok := translit[r]; ok {
			b.WriteString(repl)
			continue
		}
		if drop[r] {
			continue
		}
		if unicode.Is(unicode.Mn, r) { // drop accent/diaeresis marks
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		b.WriteRune(r)
	}
	name = b.String()

	name = whitespaceRun.ReplaceAllString(name, " ")
	name = dotRun.ReplaceAllString(name, ".")
	name = strings.Trim(name, " .")

	if windowsReserved[strings.ToUpper(strings.SplitN(name, ".", 2)[0])] {
		name = "_" + name
	}

	runes := []rune(name)
	if len(runes) > maxLen {
		runes = runes[:maxLen]
	}
	name = strings.TrimRight(string(runes), " .")
	if name == "" {
		return "untitled"
	}
	return name
}

// UniqueName returns a collision-free filename against used: `hero.png`, `hero-2.png`, ...
func UniqueName(base, ext string, used map[string]bool) string {
	name := base + ext
	for i := 2; used[name]; i++ {
		name = fmt.Sprintf("%s-%d%s", base, i, ext)
	}
	used[name] = true
	return name
}

// SubtitleBasename builds a subtitle file's basename from a step title, language and label.
func SubtitleBasename(title, srcLang, label string, index int) string {
	lang := srcLang
	if lang == "" {
		lang = label
	}
	if lang == "" {
		lang = "sub"
	}
	lang = Sanitize(strings.ToLower(lang))
	lang = strings.ReplaceAll(lang, " ", "_")
	if index != 0 {
		lang = fmt.Sprintf("%s.%d", lang, index)
	}
	return fmt.Sprintf("%s.%s.vtt", title, lang)
}
