package markdown

import (
	"fmt"
	"strings"

	"github.com/jbonadiman/futurelearn-downloader/internal/course"
)

// BuildToC renders the course's table of contents. Locked weeks are
// rendered as normal headings when scraped; weeks skipped via --skip-locked
// are listed as unlinked placeholders. sourceURL is recorded as YAML
// frontmatter so the course can be re-scraped without hunting down its URL.
func BuildToC(rootName string, steps []course.Step, locked []course.LockedWeek, sourceURL string) string {
	scrapedWeeks := make(map[string]bool, len(steps))
	for _, s := range steps {
		scrapedWeeks[s.Parts[0]] = true
	}

	var lines []string
	if sourceURL != "" {
		lines = append(lines, "---", "source: "+sourceURL, "---", "")
	}
	lines = append(lines, "# "+rootName, "")

	var curWeek, curAct string
	for _, s := range steps {
		week, act, stepdir := s.Parts[0], s.Parts[1], s.Parts[2]
		title := course.Sanitize(s.Title)
		rel := strings.Join([]string{week, act, stepdir, title + ".md"}, "/")
		if week != curWeek {
			lines = append(lines, "## "+week, "")
			curWeek, curAct = week, ""
		}
		if act != curAct {
			lines = append(lines, "### "+act, "")
			curAct = act
		}
		lines = append(lines, fmt.Sprintf("- [%s](%s)", stepdir, URLPath(rel)))
	}

	// Skipped weeks are listed but unlinked — their folders don't exist, so a link 404s.
	for _, lw := range locked {
		if scrapedWeeks[lw.FolderName] {
			continue
		}
		lines = append(lines, "", "## "+lw.FolderName, "")
		if lw.UnlocksAt != "" {
			lines = append(lines, fmt.Sprintf("*Not yet released — unlocks %s.*", lw.UnlocksAt))
		} else {
			lines = append(lines, "*Not yet released.*")
		}
	}
	lines = append(lines, "")
	return strings.Join(lines, "\n")
}
