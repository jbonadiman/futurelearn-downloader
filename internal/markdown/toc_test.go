package markdown

import (
	"strings"
	"testing"

	"github.com/jbonadiman/futurelearn-downloader/internal/course"
)

func TestBuildToC(t *testing.T) {
	steps := []course.Step{
		{Parts: []string{"Week 1 - Intro", "1. Getting started", "1.1 Hello"}, Href: "/steps/1", Title: "Hello"},
		{Parts: []string{"Week 1 - Intro", "1. Getting started", "1.2 Welcome"}, Href: "/steps/2", Title: "Welcome"},
		{Parts: []string{"Week 2 - Next", "1. More", "2.1 Deeper"}, Href: "/steps/3", Title: "Deeper"},
	}
	got := BuildToC("My Course", steps, nil, "https://www.futurelearn.com/courses/x/1/steps/1")
	for _, want := range []string{
		"---", "source: https://www.futurelearn.com/courses/x/1/steps/1", "---",
		"# My Course",
		"## Week 1 - Intro", "### 1. Getting started", "- [1.1 Hello](Week%201%20-%20Intro/1.%20Getting%20started/1.1%20Hello/Hello.md)",
		"## Week 2 - Next",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestBuildToCNoFrontmatterWhenNoSource(t *testing.T) {
	steps := []course.Step{
		{Parts: []string{"Week 1 - X", "1. Intro", "1.1 Welcome"}, Title: "Welcome"},
	}
	got := BuildToC("Course", steps, nil, "")
	if !strings.HasPrefix(got, "# Course\n") {
		t.Fatalf("expected no frontmatter, got:\n%s", got)
	}
	if strings.Contains(strings.SplitN(got, "\n", 2)[0], "---") {
		t.Fatalf("unexpected frontmatter marker on first line:\n%s", got)
	}
}

func TestBuildToCListSkippedWeeksUnlinked(t *testing.T) {
	got := BuildToC("C", nil, []course.LockedWeek{{FolderName: "Week 6 - Later", Number: 6, UnlocksAt: "20 Sep"}}, "")
	if !strings.Contains(got, "## Week 6 - Later") || !strings.Contains(got, "*Not yet released — unlocks 20 Sep.*") {
		t.Fatalf("locked week not listed as a placeholder:\n%s", got)
	}
}

func TestBuildToCScrapedWeekNotListedAsLocked(t *testing.T) {
	steps := []course.Step{
		{Parts: []string{"Week 1 - X", "1. Intro", "1.1 Welcome"}, Title: "Welcome"},
	}
	locked := []course.LockedWeek{{FolderName: "Week 1 - X", Number: 1, UnlocksAt: "1 Jan"}}
	got := BuildToC("C", steps, locked, "")
	if strings.Contains(got, "Not yet released") {
		t.Fatalf("scraped week wrongly listed as a locked placeholder:\n%s", got)
	}
}
