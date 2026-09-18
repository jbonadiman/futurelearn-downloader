package course

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// BaseURL is FutureLearn's origin, used to resolve relative step and asset URLs.
const BaseURL = "https://www.futurelearn.com"

var (
	hypernovaTreeRE = regexp.MustCompile(`(?s)<script type="application/json"[^>]*data-hypernova-key="componentsApplicationcomponentsCourseContentNavSidebar"[^>]*>\s*<!--(.*?)-->\s*</script>`)
	scriptBlobRE    = regexp.MustCompile(`(?s)<script type="application/json"[^>]*>\s*<!--(.*?)-->\s*</script>`)
	runTitleRE      = regexp.MustCompile(`"runTitle":"(.*?)"`)
)

// Week mirrors one entry of courseContent.weeks.
type Week struct {
	Number        int        `json:"number"`
	Title         string     `json:"title"`
	Locked        bool       `json:"locked"`
	WeekUnlocksAt string     `json:"weekUnlocksAt"`
	Activities    []Activity `json:"activities"`
}

// Activity mirrors one entry of a week's activities.
type Activity struct {
	Title string `json:"title"`
	Steps []Step `json:"steps"`
}

// Step mirrors one entry of an activity's steps. Parts is populated only by
// CollectSteps (the [weekName, activityName, stepName] folder path); it is
// unset on the Steps embedded directly in Activity.
type Step struct {
	StepNumber string `json:"stepNumber"`
	Title      string `json:"title"`
	Href       string `json:"href"`
	Type       string `json:"type"`
	Parts      []string
}

// LockedWeek describes one locked week: its folder name, number and unlock time.
type LockedWeek struct {
	FolderName string
	Number     int
	UnlocksAt  string
}

type courseContentEnvelope struct {
	CourseContent struct {
		Weeks []Week `json:"weeks"`
	} `json:"courseContent"`
}

// ExtractWeeks parses the course-tree JSON embedded in a course page's HTML.
func ExtractWeeks(html string) ([]Week, error) {
	var blob string
	if m := hypernovaTreeRE.FindStringSubmatch(html); m != nil {
		blob = m[1]
	} else {
		b, ok := findWeeksBlob(html)
		if !ok {
			return nil, errors.New("Could not find a course structure in this HTML.")
		}
		blob = b
	}
	var env courseContentEnvelope
	if err := json.Unmarshal([]byte(blob), &env); err != nil {
		return nil, err
	}
	return env.CourseContent.Weeks, nil
}

// findWeeksBlob scans every script blob for one whose courseContent object
// carries a "weeks" key.
func findWeeksBlob(html string) (string, bool) {
	for _, blob := range ScriptBlobs(html) {
		var probe map[string]json.RawMessage
		if err := json.Unmarshal([]byte(blob), &probe); err != nil {
			continue
		}
		cc, ok := probe["courseContent"]
		if !ok {
			continue
		}
		var ccProbe map[string]json.RawMessage
		if err := json.Unmarshal(cc, &ccProbe); err != nil {
			continue
		}
		if _, ok := ccProbe["weeks"]; !ok {
			continue
		}
		return blob, true
	}
	return "", false
}

// ScriptBlobs returns the raw JSON text of every
// `<script type="application/json">` blob embedded in html — FutureLearn's
// page-data carrier, wrapped in an HTML comment.
func ScriptBlobs(html string) []string {
	matches := scriptBlobRE.FindAllStringSubmatch(html, -1)
	out := make([]string, len(matches))
	for i, m := range matches {
		out[i] = m[1]
	}
	return out
}

// HasCourseTree reports whether html embeds a parseable course tree.
func HasCourseTree(html string) bool {
	_, err := ExtractWeeks(html)
	return err == nil
}

// RunTitle extracts the course run's title from html, falling back to "course".
func RunTitle(html string) string {
	if m := runTitleRE.FindStringSubmatch(html); m != nil {
		return m[1]
	}
	return "course"
}

// WeekFolderName returns a week's folder name: "Week N - Title", or just
// "Week N" if Title sanitises to empty.
func WeekFolderName(w Week) string {
	title := Sanitize(w.Title)
	if title != "" {
		return fmt.Sprintf("Week %d - %s", w.Number, title)
	}
	return fmt.Sprintf("Week %d", w.Number)
}

// LockedWeeks returns every locked week in weeks.
func LockedWeeks(weeks []Week) []LockedWeek {
	var out []LockedWeek
	for _, w := range weeks {
		if w.Locked {
			out = append(out, LockedWeek{
				FolderName: WeekFolderName(w),
				Number:     w.Number,
				UnlocksAt:  w.WeekUnlocksAt,
			})
		}
	}
	return out
}

// CollectSteps flattens weeks -> activities -> steps and stamps each
// Step's folder Parts.
func CollectSteps(weeks []Week, includeLocked bool) []Step {
	var out []Step
	for _, w := range weeks {
		if w.Locked && !includeLocked {
			continue
		}
		weekName := WeekFolderName(w)
		for i, act := range w.Activities {
			actName := fmt.Sprintf("%d.", i+1)
			if title := Sanitize(act.Title); title != "" {
				actName = fmt.Sprintf("%d. %s", i+1, title)
			}
			for _, step := range act.Steps {
				stepName := Sanitize(step.Title)
				if snum := strings.TrimSpace(step.StepNumber); snum != "" {
					stepName = strings.TrimSpace(snum + " " + stepName)
				}
				step.Parts = []string{weekName, actName, stepName}
				out = append(out, step)
			}
		}
	}
	return out
}

// SourceURL returns the first step's URL: a stable, re-scrapable entry
// point for this course.
func SourceURL(steps []Step) string {
	if len(steps) == 0 || steps[0].Href == "" {
		return ""
	}
	return BaseURL + steps[0].Href
}
