# futurelearn-downloader

Downloads a FutureLearn course you are enrolled in into a local folder tree: Markdown pages,
videos, subtitles, inline audio clips, inline images, quiz questions, PDF/audio downloads, and a ToC.

Built for offline study of a course you already have access to. It authenticates as *you*,
via your own browser cookies, and never submits answers or touches course state.

## Install

```bash
go install github.com/jbonadiman/futurelearn-downloader@latest
```

This installs the `futurelearn-downloader` command into `$(go env GOPATH)/bin` (usually
`~/go/bin`).

Prebuilt binaries for Linux, macOS and Windows are attached to each
[release](https://github.com/jbonadiman/futurelearn-downloader/releases).

## Requirements

- `ffmpeg` on `PATH` (videos are HLS streams, muxed by ffmpeg)
- A Netscape-format `cookies.txt` exported from your logged-in browser, covering **both**
  `futurelearn.com` and `ugc.futurelearn.com` (the subtitle CDN). The "Get cookies.txt
  LOCALLY" extension works.
- The course URL — the course home page or any step page works; each embeds the full
  course tree. If a URL doesn't render the tree directly, the first step link in the
  page is followed automatically.

## Run

```bash
futurelearn-downloader https://www.futurelearn.com/courses/japanese-rare-books-culture/7 --cookies cookies.txt -o ~/Courses
```

### Download several courses at once

Put one course URL per line in a file and pass it with `--links` — blank lines and
`#` comments are ignored, and each course is saved under its own title folder:

```bash
futurelearn-downloader --links courses.txt --cookies cookies.txt -o ~/Courses
```

```
# courses.txt
https://www.futurelearn.com/courses/japanese-rare-books-culture/7
https://www.futurelearn.com/courses/chinese-pronunciation-tone/5
```

The URL can be a course home page or a step page; if it doesn't render the course tree
directly, the first step link in the page is followed automatically.

No headless browser required.

### Options

| flag | effect |
|------|--------|
| `--links FILE` | download every course URL in `FILE` (one per line) |
| `--limit N` | only process the first N steps |
| `--skip-video` / `--skip-subs` / `--skip-downloads` | skip a media type |
| `--skip-audio` | leave inline audio clips as remote links |
| `--skip-quiz` | don't scrape quiz/test questions |
| `--skip-locked` | take only released weeks (default: scrape locked ones too) |
| `--delay SEC` | pause between step pages (default 0.3) |
| `--force` | re-scrape steps that are already complete (default: skip them) |
| `--dry-run` | print the plan, do nothing |
| `--version` | print the version and exit |

## Why TLS impersonation

FutureLearn sits behind Cloudflare bot management. A browser's `cf_clearance` cookie is bound
to that browser's TLS/JA3 fingerprint, so a plain Go `net/http` client gets 403-challenged even
with a valid `cookies.txt`. This tool impersonates a real Chrome fingerprint, so the same
`cookies.txt` is accepted directly — verified 200 on step pages, subtitle `.vtt` files, and
`/links/f/…` downloads.

If a request still gets a 403, the client rotates through a short, bounded sequence of Chrome
fingerprint profiles (spaced by `--delay`) before giving up. A run that keeps hitting 403s across
every profile means the `cf_clearance` cookie itself has expired — re-export a fresh
`cookies.txt` and re-run.

FlareSolverr is the wrong tool here: it's a headless-browser proxy (slow, frequently broken
against current Cloudflare). Fingerprint impersonation is what actually resolves the mismatch.

## Resuming

Runs are resumable — just run the same command again. A step is treated as done when its
Markdown page exists *and* every local file that page links to is present and non-empty; the
Markdown is written last and references every asset, so it doubles as the step's manifest.
Cross-references to *other* steps' Markdown pages don't count toward that check — they point at
files produced by those steps, not assets this step owns.
A completed step costs no requests at all, so a re-run over a finished course takes a fraction
of a second instead of one fetch per step plus one per quiz question.

That matters because `cf_clearance` expires after ~30 minutes: if a long run dies partway, the
resumed run spends its fresh cookie window on the steps that are actually missing.

Videos are muxed to `<name>.mp4.part` and renamed only on success, so a run killed mid-video
leaves no truncated file that a later run would mistake for a finished download.

Use `--force` to re-scrape everything, e.g. when the course content itself has been updated.

## Output layout

```
Learn Chinese - Introduction to Chinese Pronunciation and Tone/
├── ToC.md                          # nested links to every page
└── Week 1 - Introduction to Pinyin - initials and finals 1/
    └── 1. Introduction to Pinyin/
        ├── 1.1 Welcoming you to the course/
        │   ├── Welcoming you to the course.md     # title, inline <video>, transcript
        │   ├── Welcoming you to the course.mp4
        │   ├── Welcoming you to the course.en.vtt
        │   └── Tutorial for week 1.pdf
        ├── 1.7 Six Simple Finals/
        │   ├── Six Simple Finals.md
        │   ├── Six Simple Finals.mp4
        │   ├── a.mp3  o.mp3  e.mp3  i.mp3  u.mp3  u-2.mp3   # pronunciation audio (ü → u-2)
        │   └── Pinyin of six simple finals.pdf
        └── 1.10 Listen and check/
            ├── Listen and check.md                 # questions as checkbox lists
            └── Listen and check-q1-audio-1.mp3     # each question's audio clip
```

## Notes / gotchas

- Download extensions come from the `relatedFiles[].type` field (`pdf`, `audio`, …), falling
  back to the final URL's extension.
- Folder/file names are sanitised for Linux + Windows: `Ⅱ→II`, `ü→u`, smart quotes/ellipsis/`?`
  stripped, `/` and `:` replaced, trailing spaces/dots removed, Windows reserved names guarded.
- Name collisions (e.g. `u` vs `ü` both → `u`) get a `-2` suffix.
- **Inline audio** (`[Click to listen](…mp3)` links and FutureLearn's `soundcite` players) is
  downloaded as `<step>-audio-N.mp3` and embedded as `<audio controls src="…">`, the same way
  videos are. It lives in the body HTML, not in `relatedFiles`, so it needs separate handling.
- **Inline images** (body `<img>` tags and “take a closer look” links that point straight at an
  image) are downloaded into the step folder and re-linked locally, so a course renders fully
  offline instead of hot-linking the partner CDN (`fl-keio.info` and friends).
- **Related links** (`relatedLinks`, rendered as a `## Related links` section) are followed to
  their final redirect target first. FutureLearn serves them as `/links/l/<id>` short links that
  302 to the real page; the short link is stored as-is by a naive scraper and 404s once the
  course is served from anywhere else.
- **Step-to-step links** are rewritten to local paths. Glossary keyword links (e.g. a video step
  linking every term to its week's glossary `…/steps/<id>#k`) and “see step N.M” references
  point at the local `…/Step.md#anchor` instead of `futurelearn.com`, so they work offline.
- **Quizzes and tests** are scraped into a `## Quiz` section: one `### Question N` per question,
  answers as `- [ ]` checkboxes, question audio embedded. Cloze ("fill in the blanks") questions
  render the text with `**[n]**` gap markers plus a blank answer list.
  - Questions are NOT on the step page — each is at `<step>/questions/<n>`, and the question
    count comes from `quizProgressNav`. Ask for `<step>/quiz/introduction` explicitly for the
    quiz intro: once a quiz has been started, the bare step URL serves the current question.
  - Correct answers appear in the JSON (`gaveCorrectAnswer`) only **after** you have attempted
    a question, so unattempted quizzes come out with empty checkboxes. The scraper never
    submits anything.

## Legal

For personal, offline use of courses you are enrolled in. Course content remains the property
of FutureLearn and the partner institutions; this tool grants you no rights to it, and
redistributing downloaded material is on you. Check FutureLearn's terms before use.

## Licence

GNU Affero General Public License v3.0 or later — see [LICENSE](LICENSE).
