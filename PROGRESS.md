# PROGRESS — FutureLearn course scraper

Handover for the next agent. Everything below is current as of **2026-08-15 03:21**.

## TL;DR / status

**DONE and delivered.** A FutureLearn course ("Learn Chinese: Introduction to Chinese
Pronunciation and Tone", Shanghai International Studies University) has been scraped to a
local folder tree of Markdown + media and copied to João's laptop at:

```
/home/joao/Videos/Learn Chinese - Introduction to Chinese Pronunciation and Tone/
```

- 3 weeks, **47 steps**, 16 video steps
- **16** `.mp4` videos, **32** `.vtt` subtitles (EN + IT), **8** `.pdf`, **97** `.mp3`
  (9 related-file clips + 88 inline/quiz clips), **48** `.md` (47 lesson pages + `ToC.md`)
- ~1.2 GB total

The scrape is complete and João has reviewed/iterated on the Markdown output. No further
work is pending unless João asks for a follow-up (see "Open items" at the bottom).

---

## Files (all in `~/.openclaw/workspace/scratch/course-tree/`)

| file | purpose |
|------|---------|
| `scrape_course.py` | **Main scraper.** Folders + markdown + videos + subtitles + downloads + ToC. |
| `make_folders.py` | Tree builder only (stdlib); also holds shared helpers (`sanitize`, `extract_weeks`, `collect_steps`, `SCRIPT_RE`, `transcript_text`, `subtitle_basename`). Imported by the scraper. |
| `page.html` | One saved course step page. Contains the **full course tree** (`weeks→activities→steps`) in an embedded JSON blob — this is all you need to know *what* to scrape. |
| `cookies.txt` | Netscape-format session cookies exported from João's browser (**secret** — treat like a password). Required. |
| `README.md` | User-facing usage doc (kept roughly in sync). |
| `PROGRESS.md` | This file. |

## How to run

```bash
cd ~/.openclaw/workspace/scratch/course-tree
uv run --with curl_cffi --with markdownify \
  python scrape_course.py page.html --cookies cookies.txt -o /path/out
```

Flags: `--limit N`, `--skip-video/--skip-subs/--skip-downloads`, `--delay`, `--dry-run`.
`uv` is at `/usr/local/bin/uv`. No Chromium, no `LD_LIBRARY_PATH` needed. ffmpeg must be on PATH.

---

## The one thing that matters: Cloudflare bypass

FutureLearn sits behind **Cloudflare bot management**. This is the crux of the whole task.

- A browser's `cf_clearance` cookie is bound to that browser's **TLS/JA3 fingerprint**, so
  plain `curl`/`urllib`/`requests` return **403 "Just a moment"** even WITH a valid cookies.txt.
- **`curl_cffi`** impersonates a real Chrome fingerprint, so the browser-exported cookies
  work directly over plain HTTPS — no headless browser.
- **Use `impersonate="chrome124"`** (a pinned Chrome version), NOT the default `"chrome"`
  — the default is a commonly-flagged scraper profile and gets 403. Also don't override the
  User-Agent (let curl_cffi set a UA matching the impersonated fingerprint).
- **FlareSolverr is the WRONG tool** — it's a headless-browser proxy (same approach as
  Playwright, which we first tried and abandoned), and it's frequently broken vs current CF.
- **cookies expire.** `cf_clearance` shows a 2027 `expires` value in the file but is actually
  short-lived (~30 min). If a run starts returning 403, **re-export a fresh cookies.txt**
  from João's logged-in browser (the "Get cookies.txt LOCALLY" extension) and re-run. The
  script skips already-downloaded videos/subs, so re-runs are cheap.

Proven working endpoints (with `chrome124` + fresh cookies): step pages (200), `.vtt`
subtitles on `ugc.futurelearn.com` (200), `/links/f/<id>` downloads (200, redirects to
`ugc.futurelearn.com/uploads/files/...`).

---

## Scrape pipeline (`scrape_course.py`)

1. Parse the tree from `page.html` — the JSON blob is in
   `<script type="application/json" data-hypernova-key="componentsApplicationcomponentsCourseContentNavSidebar">`
   (wrapped in `<!--…-->`). `extract_weeks()` / `collect_steps()` handle this.
2. **Phase 1 (needs cookies):** for each step, `GET` the step page, extract
   `stepContent` + `relatedContent` + `videoPlayerProps`, download `.vtt` subtitles and
   `relatedFiles` (PDFs/audio), and write `{title}.md`.
3. **Phase 2 (no cookies):** ffmpeg downloads each video from
   `https://view.vzaar.com/{vzaarVideoId}/adaptive.m3u8` (this CDN is not CF-gated) with
   `-c copy -bsf:a aac_adtstoasc -movflags +faststart` → `.mp4`.
4. Write `ToC.md` (nested `## week` / `### activity` / `- [step](rel/path)` links).

### Where content lives per step (varies by component!)

- Video step → component `componentsApplicationsectionsStepsVideoArticle`; `stepContent`,
  `relatedContent`, `videoPlayerProps` are **top-level** keys in that JSON blob.
- Article/Discussion/Quiz → component `…StepsArticle`/`…StepsDiscussion`/`…StepsQuiz`;
  `stepContent`/`relatedContent` are **under `baseStep`**.
- `extract_step()` checks both `d.get("stepContent")` and `d.get("baseStep",{}).get("stepContent")`.

---

## Markdown formatting rules (iterated with João — DON'T regress these)

`stepContent.body.__html` is the body HTML. Pipeline in `body_to_markdown()`:

1. **`html.unescape()` first.** FutureLearn escapes `>` as `&gt;` (to protect the
   JSON-in-HTML comment), producing malformed `<p&gt;…`. If you skip unescape, markdownify
   returns an **empty string** (this was bug #1 João caught).
2. **Strip `<blockquote>` (unwrap).** It's FutureLearn's indentation wrapper around `<ol>`/`<ul>`
   lists, not semantic quoting. Unwrapping makes lists render as real ordered/bulleted lists
   instead of `> 1. …` blockquotes (bug #2).
3. `markdownify.markdownify(..., heading_style="ATX")`.

Then in `build_step_markdown()`:

4. **Promote the first body paragraph to `## ` (H2)** — the title is H1, the lead is H2
   (João's explicit request). Only do this if there's a second paragraph (`"\n\n" in md`);
   single-paragraph bodies stay as-is to avoid a lone heading with no body.

Final page structure: `# title` → `<video>` (if video) → `## lead` → body → `## Downloads`
→ `## Subtitles` → `## Transcript` (video only) → copyright footer.

### Naming / sanitisation (`make_folders.py::sanitize`)

`Ⅱ→II`, `ü→u`, smart quotes/ellipsis/`?` stripped, `/`→`-`, `:`→` - `, trailing spaces/dots
removed, Windows reserved names (`CON`, `NUL`…) guarded, length-capped. Name collisions
(`u` vs `ü` → both `u`) get a `-2` suffix. Download extensions come from
`relatedFiles[].type` (`pdf`, `audio`→`.mp3`) with a fallback to the final URL's extension.

---

## Delivery / environment facts

- **rhodes** (this host): scrape runs here; staging dir `/tmp/course-out`.
- **joao-laptop** (target): SSH alias `joao-laptop` = `192.168.1.122:2222`, user `joao`,
  key `~/.ssh/id_ed25519_homelab`. See `memory/joao-laptop-ssh.md` — **port 22 is dead when
  the laptop is on cable** (sshd hardcodes the wifi IP `.113`); only `.122:2222` works and
  it's a hand-started daemon that doesn't survive reboot.
- Sync command used:
  `rsync -a "/tmp/course-out/Learn Chinese - Introduction to Chinese Pronunciation and Tone/" "joao-laptop:/home/joao/Videos/Learn Chinese - Introduction to Chinese Pronunciation and Tone/"`
- Re-runs only re-transfer changed `.md` files (~27 KB), so iterating on Markdown formatting
  is cheap: edit `scrape_course.py` → re-run → re-rsync.

---

## Inline audio + quizzes (added 2026-08-15)

Both were gaps João spotted after the first delivery. Done, re-scraped into `/tmp/course-out`
and rsynced to the laptop 2026-08-15 (88 new files, 27 MB on the wire — videos matched).

**Inline audio.** Body text carries audio two ways — a plain `[Click to listen](…mp3)` link and
a `<span class="soundcite" data-url="…mp3">` player wrapper. `localize_audio()` regex-matches
audio URLs in the **raw (still HTML-escaped) body HTML**, downloads each as
`<step>-audio-N.mp3`, and rewrites the URL in place — that way the `href` *and* the `data-url`
copy both become local, and `body_to_markdown()` can swap the resulting Markdown link for an
`<audio controls src="…">` player. Doing it pre-markdownify matters: markdownify drops the
`<span>`, so a `data-url`-only clip would vanish (there's a fallback that appends the player
if no link survived). Also: don't promote a lead paragraph to `##` when it contains an
`<audio>` tag — you get a player inside a heading.

**Quizzes.** The step page has NO questions on it. Each question is its own page at
`<step>/questions/<n>`, and the count comes from `quizProgressNav.quizProgress.questionProgresses`.
Two gotchas:

- **Fetch `<step>/quiz/introduction` explicitly** for the quiz intro/weighting. Once a quiz has
  been started, the bare step URL serves the *current question* instead, whose blob has a
  stripped-down `quiz: {"isScored": false}`. Cost me a debugging round when the intro silently
  vanished between two identical-looking runs.
- **Correct answers are only revealed after an attempt** (`gaveCorrectAnswer` is `null` until
  then). The scraper renders `- [ ]` / `- [x]` from that field but **never submits** — step 3.15
  is worth 100% of the course score. If João answers the quizzes on FutureLearn and re-runs,
  the checkboxes fill themselves in.

Question types seen: `multiple_choice` (2-4 answers) and `cloze` (`textWithGaps` → text with
`**[n]**` gap markers + a blank answer list). The cloze source data sometimes omits the newline
between numbered exercises (`…g5. z`), so `cloze_text()` re-breaks on a mid-line `<n>. `.

## Open items (none blocking)

1. **Reusable for other courses** — the script should work on any FutureLearn course: save
   any step page as `page.html`, export cookies, run. Untested against courses with inline
   `<img>` in bodies (this course had none).

## Key lessons (for future-me)

- Don't hand-wave tool assumptions; probe empirically (the `chrome`→`chrome124` impersonation
  discovery came from brute-forcing `impersonate` values against the real endpoint).
- `curl_cffi` is the modern lightweight answer to Cloudflare's TLS-fingerprint challenge when
  you already have a browser's `cf_clearance` cookie. Keep it in mind for similar jobs.
- Raw memory note for this work: `memory/2026-08-14.md` (section "FutureLearn course scraper").
