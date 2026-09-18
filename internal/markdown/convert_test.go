package markdown

import (
	"regexp"
	"strings"
	"testing"
)

func TestBodyToMarkdownKeepsStructure(t *testing.T) {
	html := `<p&gt;Lead text here.</p&gt;  <ul&gt;   <li&gt;one</li>   <li&gt;two</li&gt;  </ul&gt;` +
		`<p&gt;<strong&gt;bold</strong&gt; and <em&gt;em</em> and <a href="https://x.test/a">a link</a></p&gt;` +
		`<p&gt;<img src="https://x.test/i.png" alt="alt text"/></p&gt;` +
		`<table&gt;<tbody&gt;<tr&gt;<th&gt;Term</th&gt;<th&gt;Meaning</th&gt;</tr&gt;` +
		`<tr&gt;<td&gt;k</td&gt;<td&gt;v</td&gt;</tr&gt;</tbody&gt;</table&gt;`
	md := BodyToMarkdown(html, nil)

	for _, want := range []string{
		"Lead text here.",
		"* one",
		"**bold**",
		"*em*",
		"[a link](https://x.test/a)",
		"![alt text](https://x.test/i.png)",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in:\n%s", want, md)
		}
	}
	if !regexp.MustCompile(`(?m)^\|.*Term.*\|`).MatchString(md) {
		t.Errorf("table not rendered as a table:\n%s", md)
	}
}

func TestBodyToMarkdownUnwrapsBlockquoteLists(t *testing.T) {
	// FutureLearn wraps list indentation in <blockquote>; leaving it in makes a quote.
	html := `<blockquote&gt;<ol&gt;<li&gt;first</li></ol&gt;</blockquote&gt;`
	md := BodyToMarkdown(html, nil)
	if strings.Contains(md, ">") {
		t.Fatalf("blockquote survived: %q", md)
	}
	if !strings.Contains(md, "first") {
		t.Fatalf("list lost: %q", md)
	}
}

func TestBodyToMarkdownInjectsAudioPlayers(t *testing.T) {
	html := `<p&gt;Listen: <a href="clip.mp3">clip.mp3</a></p&gt;`
	md := BodyToMarkdown(html, []string{"clip.mp3"})
	if !strings.Contains(md, `<audio controls src="clip.mp3"></audio>`) {
		t.Fatalf("audio player missing: %q", md)
	}
	if strings.Contains(md, "[clip.mp3](clip.mp3)") {
		t.Fatalf("plain link left behind: %q", md)
	}
}

func TestBodyToMarkdownUnescapesFutureLearnEntities(t *testing.T) {
	// FutureLearn escapes `>` inside its JSON-in-HTML-comment, so the body arrives as
	// `<p&gt;…` — unescaping before conversion is what makes it real HTML at all.
	html := `<p&gt;One &amp; two</p&gt;<p&gt;Three</p&gt;`
	md := BodyToMarkdown(html, nil)
	if strings.Contains(md, "&gt;") || strings.Contains(md, "&lt;") {
		t.Fatalf("entities survived as text: %q", md)
	}
	if !strings.Contains(md, "One & two") || !strings.Contains(md, "Three") {
		t.Fatalf("paragraphs lost: %q", md)
	}
}
