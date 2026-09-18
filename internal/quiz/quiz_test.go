package quiz

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jbonadiman/futurelearn-downloader/internal/course"
)

// questionBlob builds a realistic single-answer /questions/<n> JSON blob,
// letting a test override the first answer's fields — e.g. to set
// gaveCorrectAnswer.
func questionBlob(firstAnswerExtra string) string {
	extra := ""
	if firstAnswerExtra != "" {
		extra = ", " + firstAnswerExtra
	}
	return fmt.Sprintf(`{
		"type": "single-answer",
		"question": {
			"introduction": {"__html": "<p>Pick the correct answer.</p>"},
			"answers": [
				{"text": {"__html": "<p>Correct</p>"}%s},
				{"text": {"__html": "<p>Wrong</p>"}}
			],
			"allowsMultipleAnswers": false,
			"textWithGaps": [],
			"answerCount": 0
		}
	}`, extra)
}

func TestExtractQuestionWithoutAttemptRendersEmptyCheckbox(t *testing.T) {
	html := `<script type="application/json"><!--` + questionBlob(`"gaveCorrectAnswer": null`) + `--></script>`
	q, ok := ExtractQuestion(html)
	if !ok {
		t.Fatal("question not found")
	}
	md := q.Markdown(1)
	if !strings.HasPrefix(md, "### Question 1") {
		t.Fatalf("heading: %q", md)
	}
	if !strings.Contains(md, "[ ]") || strings.Contains(md, "[x]") {
		t.Fatalf("unattempted answers must be unchecked:\n%s", md)
	}
}

func TestExtractQuestionWithChosenAnswerChecksIt(t *testing.T) {
	html := `<script type="application/json"><!--` + questionBlob(`"gaveCorrectAnswer": true`) + `--></script>`
	q, ok := ExtractQuestion(html)
	if !ok {
		t.Fatal("question not found")
	}
	md := q.Markdown(2)
	if !strings.HasPrefix(md, "### Question 2") {
		t.Fatalf("heading: %q", md)
	}
	if !strings.Contains(md, "[x] Correct") {
		t.Fatalf("chosen answer must be checked:\n%s", md)
	}
	if !strings.Contains(md, "[ ] Wrong") {
		t.Fatalf("unchosen answer must stay unchecked:\n%s", md)
	}
}

func TestExtractQuizIntroWeightingAndQuestionCount(t *testing.T) {
	html := `<script type="application/json"><!--{
		"quiz": {"introduction": {"__html": "<p>Intro text.</p>"}, "stepWeightingMessage": "This counts for 10% of your grade."},
		"quizProgressNav": {"quizProgress": {"questionProgresses": [{}, {}, {}]}}
	}--></script>`
	meta, err := ExtractQuiz(html)
	if err != nil {
		t.Fatalf("ExtractQuiz: %v", err)
	}
	if meta.IntroductionHTML != "<p>Intro text.</p>" {
		t.Fatalf("introduction = %q", meta.IntroductionHTML)
	}
	if meta.Weighting != "This counts for 10% of your grade." {
		t.Fatalf("weighting = %q", meta.Weighting)
	}
	if meta.QuestionCount != 3 {
		t.Fatalf("question count = %d, want 3", meta.QuestionCount)
	}
}

func TestClozeTextRendersGapsAsNumberedBlanks(t *testing.T) {
	segs := []Segment{
		{FormattedText: "The cat sat on the "},
		{Gap: &Gap{Position: "1"}},
		{FormattedText: "."},
	}
	got := ClozeText(segs)
	if !strings.Contains(got, "**[1]**") {
		t.Fatalf("gap not rendered: %q", got)
	}
	if !strings.HasPrefix(got, "The cat sat on the ") {
		t.Fatalf("lead text lost: %q", got)
	}
}

func TestClozeTextBreaksMissingNewlineBeforeNumberedExercise(t *testing.T) {
	// A hard Markdown line break ("  \n") is expected before the inserted
	// newline.
	segs := []Segment{{FormattedText: "…g5. z"}}
	got := ClozeText(segs)
	if want := "…g  \n5. z"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestTranscriptTextJoinsParagraphsAndStripsTags(t *testing.T) {
	video := &course.Video{
		TranscriptHTML: []string{
			"<p>First paragraph.</p>",
			"<p>Second &amp; last.</p>",
		},
	}
	got := TranscriptText(video)
	want := "First paragraph.\n\nSecond & last."
	if got != want {
		t.Fatalf("transcript = %q, want %q", got, want)
	}
}

func TestTranscriptTextNilVideoIsEmpty(t *testing.T) {
	if got := TranscriptText(nil); got != "" {
		t.Fatalf("transcript = %q, want empty", got)
	}
}
