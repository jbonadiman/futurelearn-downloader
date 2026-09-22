package course

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../../internal/testdata/pages/" + name)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return string(b)
}

func TestExtractWeeksFromRealShape(t *testing.T) {
	html := loadFixture(t, "course-tree.html")
	weeks, err := ExtractWeeks(html)
	if err != nil {
		t.Fatalf("ExtractWeeks: %v", err)
	}
	if len(weeks) != 6 {
		t.Fatalf("weeks = %d, want 6", len(weeks))
	}
	steps := CollectSteps(weeks, true)
	if len(steps) != 27 {
		t.Fatalf("steps = %d, want 27", len(steps))
	}
	if locked := LockedWeeks(weeks); len(locked) != 1 {
		t.Fatalf("locked weeks = %d, want 1", len(locked))
	}
	if steps[0].Href == "" || steps[0].Title == "" || steps[0].Parts[0] == "" {
		t.Fatalf("step not populated: %+v", steps[0])
	}
}

func TestCollectStepsSanitisesStepNumber(t *testing.T) {
	// A step page's JSON is remote input; a crafted stepNumber must not turn
	// into a path that escapes the course root when the step's folder is built.
	weeks := []Week{{Number: 1, Title: "W", Activities: []Activity{{Title: "A", Steps: []Step{
		{StepNumber: "../../../../tmp/pwn", Title: "T", Href: "/courses/x/1/steps/1"},
	}}}}}
	root := t.TempDir()
	for _, s := range CollectSteps(weeks, true) {
		dir := filepath.Join(append([]string{root}, s.Parts...)...)
		if rel, err := filepath.Rel(root, dir); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("step parts escape the course root: %q", s.Parts)
		}
	}
}

func TestCollectStepsSkipsLocked(t *testing.T) {
	weeks, err := ExtractWeeks(loadFixture(t, "course-tree.html"))
	if err != nil {
		t.Fatal(err)
	}
	all := len(CollectSteps(weeks, true))
	released := len(CollectSteps(weeks, false))
	if released >= all {
		t.Fatalf("locking had no effect: all=%d released=%d", all, released)
	}
}

func TestHasCourseTreeRejectsOrdinaryPage(t *testing.T) {
	if HasCourseTree("<html><body>nope</body></html>") {
		t.Fatal("ordinary page reported as a course tree")
	}
	if !HasCourseTree(loadFixture(t, "course-tree.html")) {
		t.Fatal("tree page not detected")
	}
}

func TestSourceURLUsesFirstStepHref(t *testing.T) {
	weeks, err := ExtractWeeks(loadFixture(t, "course-tree.html"))
	if err != nil {
		t.Fatal(err)
	}
	url := SourceURL(CollectSteps(weeks, true))
	if !strings.HasPrefix(url, "https://www.futurelearn.com/") {
		t.Fatalf("source url = %q", url)
	}
}
