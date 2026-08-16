#!/usr/bin/env python3
# futurelearn-downloader — download a FutureLearn course to a local folder tree
# Copyright (C) 2026 greenteafox
#
# This program is free software: you can redistribute it and/or modify it under
# the terms of the GNU Affero General Public License as published by the Free
# Software Foundation, either version 3 of the License, or (at your option) any
# later version.
#
# This program is distributed in the hope that it will be useful, but WITHOUT ANY
# WARRANTY; without even the implied warranty of MERCHANTABILITY or FITNESS FOR A
# PARTICULAR PURPOSE. See the GNU Affero General Public License for more details.
#
# You should have received a copy of the GNU Affero General Public License along
# with this program. If not, see <https://www.gnu.org/licenses/>.
"""
Full FutureLearn course scraper (curl_cffi edition — no headless browser).

Builds the folder tree and, for every step, saves:
  - a Markdown page (title, body text converted from HTML, inline video, downloads, transcript)
  - the video (.mp4) and subtitles (.vtt) for video steps
  - inline audio clips embedded in the body text (.mp3), localised and re-linked
  - quiz/test questions as Markdown checkbox lists, with their audio clips
  - related downloads (PDFs etc.)
  - a root ToC.md linking every page, mirroring the course tree

How it bypasses Cloudflare
--------------------------
FutureLearn sits behind Cloudflare bot management. The trick is that a browser's
`cf_clearance` cookie is bound to that browser's TLS/JA3 fingerprint, so plain curl/urllib
gets 403-challenged. `curl_cffi` impersonates a real Chrome fingerprint, so the same
cookies.txt you exported from your browser is accepted directly — no headless browser,
no JS challenge, just fast HTTPS.

Caveat: `cf_clearance` expires (typically ~30 min). If the run starts hitting 403s, re-export
a fresh cookies.txt from your logged-in browser and re-run (the script skips what's already
downloaded, so it resumes cleanly).

Run:
    uv run --with "curl_cffi" --with markdownify \
      python scrape_course.py page.html --cookies cookies.txt -o /path/out
"""

import argparse
import html as H
import json
import os
import re
import subprocess
import sys
import time
import urllib.parse

import markdownify
from curl_cffi import requests as creq

import make_folders as mf

UA = ("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
      "(KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")

# Inline audio in body/question HTML: either a plain <a href="…mp3"> or a
# `<span class="soundcite" data-url="…mp3">` player wrapper (markdownify drops the span,
# so we rewrite the URL in the HTML *before* conversion and both attributes follow).
AUDIO_URL_RE = re.compile(
    r"""https?://[^\s"'<>)\]]+\.(?:mp3|m4a|wav|ogg|aac)(?:\?[^\s"'<>)\]]*)?""", re.I)

# FutureLearn `relatedFiles[].type` -> file extension (fallback: final URL's ext).
EXT_MAP = {
    "pdf": ".pdf", "audio": ".mp3", "video": ".mp4", "image": ".png",
    "doc": ".doc", "docx": ".docx", "xls": ".xls", "xlsx": ".xlsx",
    "ppt": ".ppt", "pptx": ".pptx", "zip": ".zip", "txt": ".txt",
}


def parse_cookies(path):
    """Netscape cookies.txt -> flat {name: value} dict (all cookies are futurelearn-scoped)."""
    cookies = {}
    with open(path) as f:
        for line in f:
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            p = line.split("\t")
            if len(p) < 7:
                continue
            name, value = p[5], p[6]
            cookies[name] = value
    return cookies


def make_session(cookies):
    # "chrome" (curl_cffi default) is a commonly-flagged scraper profile; a pinned
    # Chrome version passes and lets curl_cffi set a fingerprint+UA that match.
    s = creq.Session(impersonate="chrome124")
    s.cookies.update(cookies)
    return s


# ---------------------------------------------------------------------------
# Page -> structured step data
# ---------------------------------------------------------------------------

def extract_step(html_text):
    out = {"body_html": "", "copyright": "", "related_files": [], "related_links": [], "video": None}
    for m in mf.SCRIPT_RE.finditer(html_text):
        try:
            d = json.loads(m.group(1))
        except json.JSONDecodeError:
            continue
        sc = d.get("stepContent") or (d.get("baseStep") or {}).get("stepContent")
        if sc and sc.get("body") and not out["body_html"]:
            out["body_html"] = sc.get("body", {}).get("__html", "")
            out["copyright"] = (sc.get("copyright") or {}).get("__html", "")
        rc = d.get("relatedContent") or (d.get("baseStep") or {}).get("relatedContent")
        if rc is not None:
            out["related_files"] = rc.get("relatedFiles") or []
            out["related_links"] = rc.get("relatedLinks") or []
        vp = d.get("videoPlayerProps") or {}
        v = vp.get("video") or {}
        if v.get("vzaarVideoId") and not out["video"]:
            out["video"] = v
    return out


def url_path(path):
    """Percent-encode a relative path for a Markdown/HTML link target.

    Course titles are full of spaces, commas and parentheses. A Markdown destination
    ends at the first space, so `[x](Week 1 - Intro/a.md)` isn't a link at all — the
    renderer prints it verbatim. Parentheses close the destination early for the same
    reason. `quote` escapes all three (it only leaves `A-Za-z0-9_.-~` and, here, `/`).
    """
    return urllib.parse.quote(path.replace(os.sep, "/"), safe="/")


# A step page's own link targets double as its manifest — see `step_is_complete`.
MD_LINK_RE = re.compile(r"\[[^\]\n]*\]\(([^()\n]*(?:\([^()\n]*\)[^()\n]*)*)\)")
MD_SRC_RE = re.compile(r"""<(?:audio|video)\b[^>]*\bsrc="([^"]*)\"""")
REMOTE_DEST_RE = re.compile(r"^[a-zA-Z][a-zA-Z0-9+.-]*:|^#|^/")


def local_refs(md_text):
    """Local files referenced by a step page (link targets + <video>/<audio> src)."""
    for m in list(MD_LINK_RE.finditer(md_text)) + list(MD_SRC_RE.finditer(md_text)):
        dest = m.group(1)
        if not REMOTE_DEST_RE.match(dest):
            yield urllib.parse.unquote(dest)


def step_is_complete(md_path):
    """True when a step needs no further work, so a resumed run can skip its fetches.

    The Markdown is written last and links every asset the step produced, so it works
    as the manifest: page present, and every file it points at present and non-empty.
    A step whose video died in phase 2 fails this check and gets re-fetched — which it
    must, since the vzaar id only exists on the step page.
    """
    if not (os.path.exists(md_path) and os.path.getsize(md_path) > 0):
        return False
    folder = os.path.dirname(md_path)
    try:
        with open(md_path, encoding="utf-8") as fh:
            md = fh.read()
    except OSError:
        return False
    return all(
        os.path.exists(os.path.join(folder, rel)) and os.path.getsize(os.path.join(folder, rel)) > 0
        for rel in local_refs(md)
    )


def body_to_markdown(body_html, audio_files=()):
    # FutureLearn escapes `>` (as `&gt;`) inside the JSON-in-HTML-comment, which yields
    # malformed `<p&gt;…` tags — html.unescape first so markdownify sees real HTML.
    body = H.unescape(body_html)
    # `<blockquote>` is FutureLearn's indentation wrapper around `<ol>`/`<ul>` lists,
    # not semantic quoting — unwrap it so lists render as real lists.
    body = re.sub(r"</?blockquote[^>]*>", "", body)
    md = markdownify.markdownify(body, heading_style="ATX").strip()
    # Turn the (now local) audio links into real players, mirroring how videos are embedded.
    for fname in audio_files:
        player = f'<audio controls src="{url_path(fname)}"></audio>'
        md, n = re.subn(r"\[[^\]]*\]\(%s\)" % re.escape(fname), player, md)
        if not n:
            # The URL only lived in a `data-url` attribute markdownify threw away —
            # keep the clip reachable rather than silently dropping it.
            md = (md + "\n\n" + player).strip()
    return md


def extract_quiz(html_text):
    """Quiz/Test step page -> {introduction, weighting, question_count}.
    The questions themselves are NOT on this page; each lives at
    `<step href>/questions/<n>` (see `fetch_question`)."""
    out = {"introduction": "", "weighting": "", "question_count": 0}
    for m in mf.SCRIPT_RE.finditer(html_text):
        try:
            d = json.loads(m.group(1))
        except json.JSONDecodeError:
            continue
        if not isinstance(d, dict):
            continue
        q = d.get("quiz")
        if isinstance(q, dict) and q.get("introduction"):
            out["introduction"] = q["introduction"].get("__html", "")
            out["weighting"] = q.get("stepWeightingMessage") or ""
        nav = d.get("quizProgressNav") or {}
        progs = (nav.get("quizProgress") or {}).get("questionProgresses") or []
        if progs:
            out["question_count"] = len(progs)
    return out


def extract_question(html_text):
    """A `/questions/<n>` page -> {type, introduction, answers, text_with_gaps, answer_count}."""
    for m in mf.SCRIPT_RE.finditer(html_text):
        try:
            d = json.loads(m.group(1))
        except json.JSONDecodeError:
            continue
        if not isinstance(d, dict) or "question" not in d:
            continue
        q = d["question"] or {}
        return {
            "type": d.get("type", ""),
            "introduction": (q.get("introduction") or {}).get("__html", ""),
            "answers": q.get("answers") or [],
            "multiple": bool(q.get("allowsMultipleAnswers")),
            "text_with_gaps": q.get("textWithGaps") or [],
            "answer_count": q.get("answerCount") or 0,
        }
    return None


def cloze_text(segments):
    """`textWithGaps` -> markdown, gaps rendered as numbered blanks."""
    parts = []
    for seg in segments:
        if "gap" in seg:
            # No padding spaces: gaps sit inside a syllable (`**[1]**ǎo`).
            parts.append("**[%s]**" % seg["gap"].get("position", "?"))
        else:
            parts.append(seg.get("formattedText", ""))
    text = "".join(parts).replace("\r\n", "\n").replace("\r", "\n")
    # The source sometimes forgets the newline between numbered exercises ("…g5. z") —
    # break before a mid-line "<n>. " so each exercise gets its own line.
    text = re.sub(r"(?<=\S)[ \t]*(\d+)\.[ \t]*", r"\n\1. ", text)
    # Single newlines are significant here (one exercise per line) — force hard breaks.
    return "\n".join(ln.rstrip() + "  " for ln in text.split("\n") if ln.strip()).strip()


def transcript_text(video):
    paras = (video.get("englishHtmlTranscript") or {}).get("paragraphs") or []
    out = []
    for p in paras:
        t = p.get("text", {}).get("__html", "")
        t = re.sub(r"<[^>]+>", "", H.unescape(t)).strip()
        if t:
            out.append(t)
    return "\n\n".join(out)


# ---------------------------------------------------------------------------
# Fetch + download helpers
# ---------------------------------------------------------------------------

def fetch_text(session, url):
    r = session.get(url, timeout=60)
    r.raise_for_status()
    return r.text


def download_bytes(session, url, dest):
    r = session.get(url, timeout=120)
    r.raise_for_status()
    with open(dest, "wb") as fh:
        fh.write(r.content)
    return os.path.getsize(dest) > 0


def unique_name(base, ext, used):
    """Reserve a collision-free filename within a step ("u" vs "ü" both sanitise to "u").

    Deterministic: the same step processed in the same order yields the same suffixes,
    which is what lets a resumed run recognise a file it wrote on an earlier pass.
    """
    fname = base + ext
    k = 2
    while fname in used:
        fname = f"{base}-{k}{ext}"
        k += 1
    used.add(fname)
    return fname


def download_related(session, link_url, ftype, title_sane, folder, used):
    """Download a related file, deriving its extension from type or the final URL.

    Skips the transfer when the file is already on disk. When `relatedFiles[].type` is
    known the name is derivable up front, so the GET is skipped too; otherwise the
    extension only appears once the `/links/f/…` redirect resolves and we have to ask.
    """
    ext = EXT_MAP.get(ftype)
    r = None
    if not ext:
        r = session.get(link_url, timeout=120)
        r.raise_for_status()
        ext = os.path.splitext(r.url.split("?")[0])[1] or ".bin"
    if not ext.startswith("."):
        ext = "." + ext

    fname = unique_name(title_sane, ext, used)
    dest = os.path.join(folder, fname)
    if os.path.exists(dest) and os.path.getsize(dest) > 0:
        return fname

    if r is None:
        r = session.get(link_url, timeout=120)
        r.raise_for_status()
    with open(dest, "wb") as fh:
        fh.write(r.content)
    return fname


def localize_audio(session, body_html, folder, base, used):
    """Download every inline audio clip in `body_html` and rewrite its URL to the local
    filename. Returns `(html, [filenames])` in document order — pass the filenames to
    `body_to_markdown()` so they become <audio> players."""
    # Match against the raw (still HTML-escaped) body so the replacement lands on both the
    # href="…" and the data-url="…" copy; unescape only what we hand to the HTTP client.
    urls = []
    for m in AUDIO_URL_RE.finditer(body_html):
        if m.group(0) not in urls:
            urls.append(m.group(0))

    names = []
    for raw in urls:
        u = H.unescape(raw)
        ext = os.path.splitext(u.split("?")[0])[1].lower() or ".mp3"
        fname = f"{base}-audio-{len(names) + 1}{ext}"
        k = 2
        while fname in used:
            fname = f"{base}-audio-{len(names) + 1}-{k}{ext}"
            k += 1
        dest = os.path.join(folder, fname)
        if not (os.path.exists(dest) and os.path.getsize(dest) > 0):
            try:
                download_bytes(session, u, dest)
                print(f"      audio -> {fname}")
            except Exception as e:
                print(f"      ! audio failed: {e}", file=sys.stderr)
                continue
        used.add(fname)
        names.append(fname)
        body_html = body_html.replace(raw, fname)
    return body_html, names


def download_video(vid, dest_mp4):
    """Mux the HLS stream to `dest_mp4`, atomically.

    ffmpeg is writing a growing file, so a run killed mid-video leaves a non-empty but
    truncated .mp4 — which the resume check would then read as "already downloaded" and
    skip forever. Writing to `.part` and renaming on success means the final name only
    ever exists for a complete video. `-f mp4` is required because the `.part`
    extension gives ffmpeg no format to infer.
    """
    url = f"https://view.vzaar.com/{vid}/adaptive.m3u8"
    part = dest_mp4 + ".part"
    cmd = ["ffmpeg", "-y", "-loglevel", "error", "-i", url,
           "-c", "copy", "-bsf:a", "aac_adtstoasc", "-movflags", "+faststart",
           "-f", "mp4", part]
    r = subprocess.run(cmd, capture_output=True, text=True)
    ok = r.returncode == 0 and os.path.exists(part) and os.path.getsize(part) > 0
    if ok:
        os.replace(part, dest_mp4)
    else:
        print(f"      ! ffmpeg failed: {r.stderr.strip()[:300]}", file=sys.stderr)
        if os.path.exists(part):
            os.remove(part)
    return ok


# ---------------------------------------------------------------------------
# Markdown assembly
# ---------------------------------------------------------------------------

def scrape_quiz(session, step_url, folder, base, used, delay=0.3):
    """Fetch a Quiz/Test step's questions and render them as Markdown checkbox lists.

    Answers are only known once the question has been attempted on FutureLearn
    (`gaveCorrectAnswer` is null otherwise), so unattempted quizzes come out as empty
    checkboxes. Nothing is ever submitted — the final test is worth 100% of the course
    score and answering it from a scraper would burn João's real attempts.
    """
    # Ask for `/quiz/introduction` explicitly: once a quiz has been started, the bare step
    # URL serves the current *question* instead, whose blob has no quiz introduction.
    try:
        meta = extract_quiz(fetch_text(session, step_url + "/quiz/introduction"))
    except Exception as e:
        print(f"      ! quiz fetch failed: {e}", file=sys.stderr)
        return []

    lines = ["## Quiz", ""]
    if meta["introduction"]:
        html, audio = localize_audio(session, meta["introduction"], folder,
                                     f"{base}-intro", used)
        intro = body_to_markdown(html, audio)
        if intro:
            lines += [intro, ""]
    if meta["weighting"]:
        lines += [f"*{meta['weighting']}*", ""]

    for n in range(1, meta["question_count"] + 1):
        try:
            q = extract_question(fetch_text(session, f"{step_url}/questions/{n}"))
        except Exception as e:
            print(f"      ! question {n} failed: {e}", file=sys.stderr)
            continue
        if not q:
            continue
        print(f"      question {n}/{meta['question_count']} ({q['type']})")

        lines.append(f"### Question {n}")
        lines.append("")
        if q["introduction"]:
            html, audio = localize_audio(session, q["introduction"], folder,
                                         f"{base}-q{n}", used)
            intro = body_to_markdown(html, audio)
            if intro:
                lines += [intro, ""]

        if q["type"] == "cloze":
            body = cloze_text(q["text_with_gaps"])
            if body:
                lines += [body, ""]
            if q["answer_count"]:
                lines.append("**Answers:**")
                lines.append("")
                for i in range(1, q["answer_count"] + 1):
                    lines.append(f"{i}. \\_\\_\\_\\_\\_\\_")
                lines.append("")
        else:
            for a in q["answers"]:
                text = body_to_markdown((a.get("text") or {}).get("__html", ""))
                text = " ".join(text.split())
                # `gaveCorrectAnswer` stays null until the question is attempted.
                mark = "x" if a.get("gaveCorrectAnswer") else " "
                lines.append(f"- [{mark}] {text}")
            lines.append("")
        time.sleep(delay)

    return lines


def build_step_markdown(title, data, title_sane, sub_links, download_links,
                        audio_files=(), quiz_lines=()):
    lines = [f"# {title}", ""]
    video = data["video"]
    if video:
        lines.append(f'<video controls src="{url_path(title_sane + ".mp4")}"></video>')
        lines.append("")

    if data["body_html"]:
        body_md = body_to_markdown(data["body_html"], audio_files)
        # The first paragraph is a lead/subtitle under the H1 title — promote it to H2,
        # unless it carries an audio player (a heading is no place for one).
        if "\n\n" in body_md and "<audio" not in body_md.split("\n\n", 1)[0]:
            lead, rest = body_md.split("\n\n", 1)
            lines.append(f"## {lead.strip()}")
            lines.append("")
            if rest.strip():
                lines.append(rest.strip())
        else:
            lines.append(body_md)
        lines.append("")

    if quiz_lines:
        lines.extend(quiz_lines)

    if download_links:
        lines.append("## Downloads")
        lines.append("")
        for label, fname in download_links:
            lines.append(f"- [{label}]({url_path(fname)})")
        lines.append("")

    if data["related_links"]:
        lines.append("## Related links")
        lines.append("")
        for lnk in data["related_links"]:
            lines.append(f"- [{lnk.get('title', lnk.get('url', 'link'))}]({lnk.get('url', '')})")
        lines.append("")

    if sub_links:
        lines.append("## Subtitles")
        lines.append("")
        for name in sub_links:
            lines.append(f"- [{name}]({url_path(name)})")
        lines.append("")

    if video:
        transcript = transcript_text(video)
        if transcript:
            lines.append("## Transcript")
            lines.append("")
            lines.append(transcript)
            lines.append("")

    if data["copyright"]:
        copy_txt = re.sub(r"<[^>]+>", "", H.unescape(data["copyright"])).strip()
        if copy_txt:
            lines.append("---")
            lines.append("")
            lines.append(copy_txt)
            lines.append("")

    return "\n".join(lines).rstrip() + "\n"


def build_toc(root_name, items, locked=()):
    """Render the ToC. Locked weeks are annotated in place when they were scraped
    anyway, and listed as unlinked placeholders when they were skipped."""
    unlock_by_week = {name: when for name, _, when in locked}
    scraped_weeks = {parts[0] for parts, _ in items}
    lines = [f"# {root_name}", ""]
    cur_week = cur_act = None
    for parts, step in items:
        week, act, stepdir = parts
        title = mf.sanitize(step.get("title", ""))
        rel = os.path.join(week, act, stepdir, f"{title}.md")
        if week != cur_week:
            note = unlock_by_week.get(week)
            lines.append(f"## {week}" + (f" — not yet released publicly (unlocks {note})"
                                         if note else ""))
            lines.append("")
            cur_week, cur_act = week, None
        if act != cur_act:
            lines.append(f"### {act}")
            lines.append("")
            cur_act = act
        lines.append(f"- [{stepdir}]({url_path(rel)})")
    # Skipped weeks are listed but unlinked — their folders don't exist, so a link 404s.
    for wname, wnum, when in locked:
        if wname in scraped_weeks:
            continue
        lines.append("")
        lines.append(f"## {wname}")
        lines.append("")
        lines.append(f"*Not yet released — unlocks {when}.*" if when
                     else "*Not yet released.*")
    lines.append("")
    return "\n".join(lines)


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main():
    ap = argparse.ArgumentParser(description="Scrape a FutureLearn course to folders + markdown.")
    ap.add_argument("html_file", nargs="?", default="page.html")
    ap.add_argument("--cookies", required=True)
    ap.add_argument("-o", "--out", default=".")
    ap.add_argument("--limit", type=int, default=0, help="process first N steps (0=all)")
    ap.add_argument("--delay", type=float, default=0.3)
    ap.add_argument("--skip-video", action="store_true")
    ap.add_argument("--skip-subs", action="store_true")
    ap.add_argument("--skip-downloads", action="store_true")
    ap.add_argument("--skip-audio", action="store_true", help="don't localise inline audio clips")
    ap.add_argument("--skip-quiz", action="store_true", help="don't scrape quiz/test questions")
    ap.add_argument("--force", action="store_true",
                    help="re-scrape steps that are already complete (default: skip them)")
    ap.add_argument("--skip-locked", action="store_true",
                    help="take only released weeks (default: scrape locked ones too)")
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()

    with open(args.html_file, encoding="utf-8", errors="replace") as fh:
        html_text = fh.read()

    weeks = mf.extract_weeks(html_text)
    root_name = mf.sanitize(mf.extract_run_title(html_text))
    root = os.path.join(args.out, root_name)
    items = mf.collect_steps(weeks, include_locked=not args.skip_locked)
    locked = mf.locked_weeks(weeks)

    if locked:
        verb = "skipping" if args.skip_locked else "scraping anyway"
        print(f"WARNING: {len(locked)} week(s) not yet released to you — {verb}:")
        for wname, wnum, when in locked:
            print(f"  - Week {wnum}: {wname}" + (f"  (unlocks {when})" if when else ""))
        if args.skip_locked:
            print("Re-run after they open; completed steps are skipped, so only the new "
                  "weeks are fetched.\n")
        else:
            print("WARNING: these weeks are gated in the UI but their content is still\n"
                  "         served to an enrolled session, so it is being downloaded now.\n"
                  "         Pass --skip-locked to take only what has been released.\n")

    for parts, _ in items:
        os.makedirs(os.path.join(root, *parts), exist_ok=True)

    if args.limit:
        items = items[: args.limit]

    if args.dry_run:
        print(f"Would scrape {len(items)} step(s) into: {root}")
        return

    session = make_session(parse_cookies(args.cookies))
    video_jobs = []  # (vzaarVideoId, dest) — deferred to phase 2 (no cookies needed)
    skipped = 0

    # ---- Phase 1: pages + subtitles + downloads (needs cookies) ----
    for i, (parts, step) in enumerate(items):
        folder = os.path.join(root, *parts)
        title = step.get("title", "")
        title_sane = mf.sanitize(title)
        md_path = os.path.join(folder, f"{title_sane}.md")
        # Every asset written into this step's folder, so downloads/audio can't collide
        # (e.g. "u" and "ü" both sanitise to "u").
        used_names = set()
        if not args.force and step_is_complete(md_path):
            print(f"[{i+1}/{len(items)}] {step.get('stepNumber')} {title} -- complete, skipping")
            skipped += 1
            continue
        print(f"[{i+1}/{len(items)}] {step.get('stepNumber')} {title}")

        url = mf.BASE_URL + step.get("href", "")
        try:
            html = fetch_text(session, url)
        except Exception as e:
            print(f"      ! fetch failed: {e}", file=sys.stderr)
            continue

        data = extract_step(html)

        if data["video"] and not args.skip_video:
            vid = data["video"]["vzaarVideoId"]
            dest = os.path.join(folder, f"{title_sane}.mp4")
            if not (os.path.exists(dest) and os.path.getsize(dest) > 0):
                video_jobs.append((vid, dest))

        sub_links = []
        if data["video"] and not args.skip_subs:
            seen = {}
            for sub in data["video"].get("subtitles", []):
                src = sub.get("src")
                if not src:
                    continue
                lang = (sub.get("srcLang") or sub.get("label") or "sub").lower()
                seen[lang] = seen.get(lang, 0) + 1
                fname = mf.subtitle_basename(title_sane, sub, seen[lang] - 1)
                sub_links.append(fname)
                dest = os.path.join(folder, fname)
                if os.path.exists(dest) and os.path.getsize(dest) > 0:
                    continue
                try:
                    txt = fetch_text(session, src)
                    with open(dest, "w", encoding="utf-8") as fh:
                        fh.write(txt.strip() + "\n")
                except Exception as e:
                    print(f"      ! subtitle failed: {e}", file=sys.stderr)

        download_links = []
        if data["related_files"] and not args.skip_downloads:
            for f in data["related_files"]:
                label = f.get("title", "download")
                title_sane_f = mf.sanitize(label)
                try:
                    fname = download_related(session, mf.BASE_URL + f.get("url", ""),
                                             f.get("type"), title_sane_f, folder, used_names)
                    download_links.append((label, fname))
                    print(f"      download -> {fname}")
                except Exception as e:
                    print(f"      ! download failed: {e}", file=sys.stderr)

        # Inline audio clips (`[Click to listen](…mp3)`, soundcite players) live in the body
        # text rather than in relatedFiles, so they need localising separately.
        audio_files = []
        if not args.skip_audio and data["body_html"]:
            data["body_html"], audio_files = localize_audio(
                session, data["body_html"], folder, title_sane, used_names)

        quiz_lines = []
        if step.get("type") in ("Quiz", "Test") and not args.skip_quiz:
            quiz_lines = scrape_quiz(session, url, folder, title_sane, used_names, args.delay)

        md = build_step_markdown(title, data, title_sane, sub_links, download_links,
                                 audio_files, quiz_lines)
        with open(md_path, "w", encoding="utf-8") as fh:
            fh.write(md)

        time.sleep(args.delay)

    # ---- Phase 2: videos via ffmpeg (no cookies needed) ----
    for j, (vid, dest) in enumerate(video_jobs, 1):
        print(f"  [video {j}/{len(video_jobs)}] {os.path.basename(dest)}")
        download_video(vid, dest)

    # ---- ToC ----
    toc = build_toc(root_name, items, locked)
    with open(os.path.join(root, "ToC.md"), "w", encoding="utf-8") as fh:
        fh.write(toc)

    print(f"\nDone. Course saved under: {root}"
          + (f" ({skipped} step(s) already complete, skipped)" if skipped else ""))
    if locked:
        nxt = ", ".join(f"Week {n} on {w}" if w else f"Week {n}" for _, n, w in locked)
        if args.skip_locked:
            print(f"Still locked: {nxt}. Re-run then to pick them up.")
        else:
            print(f"WARNING: included {len(locked)} week(s) not yet released to you "
                  f"({nxt}).")


if __name__ == "__main__":
    main()
