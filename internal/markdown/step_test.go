package markdown

import (
	"strings"
	"testing"
)

// A step body that opens with an h1 matching the title must not repeat it.

func TestBodyOpeningH1MatchingTitleIsNotDuplicated(t *testing.T) {
	md := BuildStepMarkdown(StepInput{
		Title: "Are Video Games Art?", TitleSane: "Are Video Games Art",
		BodyHTML: "<h1>Are Video Games Art?</h1><p><strong>By Aaron Smuts</strong></p>",
	})
	if n := strings.Count(md, "# Are Video Games Art?"); n != 1 {
		t.Fatalf("title duplicated (count=%d):\n%s", n, md)
	}
}

func TestBodyOpeningH1WithExtraSubtitleIsNotDuplicated(t *testing.T) {
	md := BuildStepMarkdown(StepInput{
		Title: "Game Engines", TitleSane: "Game Engines",
		BodyHTML: "<h1>Game Engines in Scientific Research</h1><p>Intro.</p>",
	})
	if n := strings.Count(md, "# Game Engines"); n != 1 {
		t.Fatalf("title duplicated (count=%d):\n%s", n, md)
	}
	if !strings.Contains(md, "Intro.") {
		t.Fatalf("intro paragraph lost:\n%s", md)
	}
}

func TestUnrelatedOpeningH1IsKept(t *testing.T) {
	md := BuildStepMarkdown(StepInput{
		Title: "Discuss and share with us", TitleSane: "Discuss and share with us",
		BodyHTML: "<h1>Wo de zhou mo</h1><p>Content.</p>",
	})
	if !strings.Contains(md, "# Discuss and share with us") {
		t.Fatalf("step title lost:\n%s", md)
	}
	if !strings.Contains(md, "# Wo de zhou mo") {
		t.Fatalf("unrelated body heading lost:\n%s", md)
	}
}

func TestBodyOpeningH1WithCurlyQuotesIsNotDuplicated(t *testing.T) {
	md := BuildStepMarkdown(StepInput{
		Title:     `"Black Myth: Wukong": How Did One Game Become a Global Sensation? `,
		TitleSane: "Black Myth - Wukong",
		BodyHTML:  "<h1>“Black Myth: Wukong”: How Did One Game Become a Global Sensation?</h1><p>Text.</p>",
	})
	if n := strings.Count(md, "Black Myth"); n != 1 {
		t.Fatalf("curly-quoted heading not deduped (count=%d):\n%s", n, md)
	}
}

func TestUnrelatedH2LeadIsKept(t *testing.T) {
	md := BuildStepMarkdown(StepInput{
		Title: "A step", TitleSane: "A step",
		BodyHTML: "<h2>Poll question</h2><p>Content.</p>",
	})
	if !strings.Contains(md, "## Poll question") {
		t.Fatalf("poll heading lost:\n%s", md)
	}
}
