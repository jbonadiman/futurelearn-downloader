// Package quiz handles Quiz/Test steps: quiz metadata, per-question
// extraction, cloze rendering and transcript text. Answers are only known
// once a question is attempted on FutureLearn, so nothing here ever
// submits one — an unattempted question always renders with empty
// checkboxes.
package quiz

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/jbonadiman/futurelearn-downloader/internal/course"
	"github.com/jbonadiman/futurelearn-downloader/internal/markdown"
)

// Meta is a Quiz/Test step page's own metadata. The questions themselves
// are NOT on this page; each lives at "<step href>/questions/<n>".
type Meta struct {
	IntroductionHTML string
	Weighting        string
	QuestionCount    int
}

// Answer mirrors one entry of question.answers.
type Answer struct {
	TextHTML          string
	GaveCorrectAnswer bool
}

// Gap mirrors a textWithGaps segment's "gap" object.
type Gap struct {
	Position string `json:"position"`
}

// Segment mirrors one entry of question.textWithGaps: either a gap or a
// plain formatted-text run.
type Segment struct {
	Gap           *Gap   `json:"gap,omitempty"`
	FormattedText string `json:"formattedText,omitempty"`
}

// Question is a quiz question's extracted content.
type Question struct {
	Type             string
	IntroductionHTML string
	Answers          []Answer
	Multiple         bool
	TextWithGaps     []Segment
	AnswerCount      int
}

type rawHTML struct {
	HTML string `json:"__html"`
}

type quizProbe struct {
	Introduction         *rawHTML `json:"introduction"`
	StepWeightingMessage string   `json:"stepWeightingMessage"`
}

type quizProgressNavProbe struct {
	QuizProgress *struct {
		QuestionProgresses []json.RawMessage `json:"questionProgresses"`
	} `json:"quizProgress"`
}

type quizBlob struct {
	Quiz            *quizProbe            `json:"quiz"`
	QuizProgressNav *quizProgressNavProbe `json:"quizProgressNav"`
}

func ExtractQuiz(html string) (Meta, error) {
	var meta Meta
	for _, raw := range course.ScriptBlobs(html) {
		var blob quizBlob
		if err := json.Unmarshal([]byte(raw), &blob); err != nil {
			continue
		}
		// Presence of the introduction object matters, not its HTML content —
		// {"__html": ""} is still a populated object.
		if blob.Quiz != nil && blob.Quiz.Introduction != nil {
			meta.IntroductionHTML = blob.Quiz.Introduction.HTML
			meta.Weighting = blob.Quiz.StepWeightingMessage
		}
		if blob.QuizProgressNav != nil && blob.QuizProgressNav.QuizProgress != nil {
			if n := len(blob.QuizProgressNav.QuizProgress.QuestionProgresses); n > 0 {
				meta.QuestionCount = n
			}
		}
	}
	return meta, nil
}

type answerProbe struct {
	Text              *rawHTML `json:"text"`
	GaveCorrectAnswer bool     `json:"gaveCorrectAnswer"`
}

type questionProbe struct {
	Introduction          *rawHTML      `json:"introduction"`
	Answers               []answerProbe `json:"answers"`
	AllowsMultipleAnswers bool          `json:"allowsMultipleAnswers"`
	TextWithGaps          []Segment     `json:"textWithGaps"`
	AnswerCount           int           `json:"answerCount"`
}

type questionBlobShape struct {
	Type     string         `json:"type"`
	Question *questionProbe `json:"question"`
}

// ExtractQuestion extracts a "/questions/<n>" page's question, or
// ok=false if none is embedded.
func ExtractQuestion(html string) (*Question, bool) {
	for _, raw := range course.ScriptBlobs(html) {
		var probe map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &probe); err != nil {
			continue
		}
		if _, ok := probe["question"]; !ok {
			continue
		}
		var blob questionBlobShape
		if err := json.Unmarshal([]byte(raw), &blob); err != nil {
			continue
		}
		q := &Question{Type: blob.Type}
		if blob.Question == nil {
			return q, true
		}
		if blob.Question.Introduction != nil {
			q.IntroductionHTML = blob.Question.Introduction.HTML
		}
		q.Multiple = blob.Question.AllowsMultipleAnswers
		q.TextWithGaps = blob.Question.TextWithGaps
		q.AnswerCount = blob.Question.AnswerCount
		for _, a := range blob.Question.Answers {
			ans := Answer{GaveCorrectAnswer: a.GaveCorrectAnswer}
			if a.Text != nil {
				ans.TextHTML = a.Text.HTML
			}
			q.Answers = append(q.Answers, ans)
		}
		return q, true
	}
	return nil, false
}

var numberedLineRE = regexp.MustCompile(`(\S)[ \t]*(\d+)\.[ \t]*`)

// ClozeText renders textWithGaps as Markdown, gaps rendered as numbered
// blanks.
func ClozeText(segments []Segment) string {
	var b strings.Builder
	for _, seg := range segments {
		if seg.Gap != nil {
			pos := seg.Gap.Position
			if pos == "" {
				pos = "?"
			}
			fmt.Fprintf(&b, "**[%s]**", pos)
		} else {
			b.WriteString(seg.FormattedText)
		}
	}
	text := b.String()
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	// The source sometimes forgets the newline between numbered exercises
	// ("…g5. z") — break before a mid-line "<n>. " so each exercise gets its
	// own line.
	text = numberedLineRE.ReplaceAllString(text, "$1\n$2. ")

	var lines []string
	for _, ln := range strings.Split(text, "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		// Single newlines are significant here (one exercise per line) —
		// force hard breaks.
		lines = append(lines, strings.TrimRight(ln, " \t")+"  ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// TranscriptText renders video's transcript as plain text.
func TranscriptText(video *course.Video) string {
	return course.TranscriptText(video)
}

// Markdown renders one question as a "### Question N" block. audioFiles
// is variadic so the common caller passes none (the pure-extraction unit
// tests need no audio); an orchestrator that already localised inline
// audio for this question's introduction can pass the local filenames
// through, same as markdown.StepInput.AudioFiles.
func (q *Question) Markdown(n int, audioFiles ...string) string {
	var lines []string
	lines = append(lines, fmt.Sprintf("### Question %d", n), "")

	if q.IntroductionHTML != "" {
		if intro := markdown.BodyToMarkdown(q.IntroductionHTML, audioFiles); intro != "" {
			lines = append(lines, intro, "")
		}
	}

	if q.Type == "cloze" {
		if body := ClozeText(q.TextWithGaps); body != "" {
			lines = append(lines, body, "")
		}
		if q.AnswerCount > 0 {
			lines = append(lines, "**Answers:**", "")
			for i := 1; i <= q.AnswerCount; i++ {
				lines = append(lines, fmt.Sprintf(`%d. \_\_\_\_\_\_`, i))
			}
			lines = append(lines, "")
		}
	} else {
		for _, a := range q.Answers {
			text := markdown.BodyToMarkdown(a.TextHTML, nil)
			text = strings.Join(strings.Fields(text), " ")
			mark := " "
			if a.GaveCorrectAnswer {
				mark = "x"
			}
			lines = append(lines, fmt.Sprintf("- [%s] %s", mark, text))
		}
		lines = append(lines, "")
	}

	return strings.Join(lines, "\n")
}
