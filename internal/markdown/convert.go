// Package markdown converts FutureLearn step body HTML to Markdown.
package markdown

import (
	"fmt"
	"html"
	"regexp"
	"strings"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
)

var blockquoteTagRE = regexp.MustCompile(`</?blockquote[^>]*>`)

// html-to-markdown/v2's "smart" escape mode escapes a much wider set
// (`[]()#>-~=+|` and more) than needed here. Plain text should only need
// `*` and `_` escaped to guard against accidental emphasis, leaving `[`,
// `]`, `#`, `-`, backticks, etc. untouched — so the smart mode is disabled
// and replaced with that narrower rule below.
var conv = converter.NewConverter(converter.WithPlugins(
	base.NewBasePlugin(),
	commonmark.NewCommonmarkPlugin(
		commonmark.WithBulletListMarker("*"),
		commonmark.WithHorizontalRule("---"), // the canonical horizontal-rule marker
	),
	table.NewTablePlugin(),
), converter.WithEscapeMode(converter.EscapeModeDisabled))

var asteriskUnderscoreReplacer = strings.NewReplacer("*", `\*`, "_", `\_`)

func init() {
	conv.Register.TextTransformer(func(_ converter.Context, content string) string {
		return asteriskUnderscoreReplacer.Replace(content)
	}, converter.PriorityLate)
}

// BodyToMarkdown converts a step's body HTML into Markdown, rewriting any
// already-localised audio links into inline players.
func BodyToMarkdown(bodyHTML string, audioFiles []string) string {
	// FutureLearn escapes `>` (as `&gt;`) inside the JSON-in-HTML-comment, which
	// yields malformed `<p&gt;…` tags — unescape first so the converter sees real HTML.
	body := html.UnescapeString(bodyHTML)
	// `<blockquote>` is FutureLearn's indentation wrapper around `<ol>`/`<ul>`
	// lists, not semantic quoting — unwrap it so lists render as real lists.
	body = blockquoteTagRE.ReplaceAllString(body, "")

	md, err := conv.ConvertString(body)
	if err != nil {
		return ""
	}
	// The renderer unconditionally escapes `[`/`]` in image alt text, guarding
	// against malformed markdown — undo it here since nothing else in this
	// output escapes brackets.
	md = strings.NewReplacer(`\[`, "[", `\]`, "]").Replace(md)
	md = strings.TrimSpace(md)

	// Turn the (now local) audio links into real players, mirroring how videos
	// are embedded.
	linkRECache := make(map[string]*regexp.Regexp)
	for _, fname := range audioFiles {
		pattern := `\[[^\]]*\]\(` + regexp.QuoteMeta(fname) + `\)`
		if _, ok := linkRECache[pattern]; !ok {
			linkRECache[pattern] = regexp.MustCompile(pattern)
		}
		linkRE := linkRECache[pattern]
		player := fmt.Sprintf(`<audio controls src="%s"></audio>`, URLPath(fname))
		replaced := linkRE.ReplaceAllString(md, player)
		if replaced != md {
			md = replaced
			continue
		}
		// The URL only lived in a `data-url` attribute the converter threw away —
		// keep the clip reachable rather than silently dropping it.
		md = strings.TrimSpace(md + "\n\n" + player)
	}
	return md
}

// isURLUnreserved matches RFC 3986's unreserved set (letters, digits,
// "_.-~") — narrower than Go's url.PathEscape, which also leaves RFC 3986
// sub-delims like "&" and "," unescaped in path segments.
func isURLUnreserved(b byte) bool {
	switch {
	case b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z', b >= '0' && b <= '9':
		return true
	case b == '_' || b == '.' || b == '-' || b == '~':
		return true
	default:
		return false
	}
}

// URLPath percent-encodes a relative path for a Markdown/HTML link target,
// preserving "/" as a separator.
func URLPath(path string) string {
	path = strings.ReplaceAll(path, `\`, "/")
	var b strings.Builder
	for i := 0; i < len(path); i++ {
		c := path[i]
		if c == '/' || isURLUnreserved(c) {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}
