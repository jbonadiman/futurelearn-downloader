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
    futurelearn <course-url> --cookies cookies.txt -o /path/out

    # or download several courses from a list of URLs (one per line):
    futurelearn --links courses.txt --cookies cookies.txt -o /path/out

(or, without installing: `python -m futurelearn_downloader <course-url> --cookies cookies.txt`)
"""

import argparse
import concurrent.futures
import html as H
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import threading
import time
import urllib.parse

import markdownify
from curl_cffi import requests as creq
from tqdm import tqdm

from . import make_folders as mf

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

# Inline images in body HTML: `<img src="…">` and "take a closer look" `<a href>`
# links that point straight at an image on the partner CDN.
IMG_TAG_SRC_RE = re.compile(r'<img\b[^>]*?\bsrc\s*=\s*(["\'])(.*?)\1', re.I | re.S)
A_TAG_HREF_RE = re.compile(r'<a\b[^>]*?\bhref\s*=\s*(["\'])(.*?)\1', re.I | re.S)
IMAGE_EXT_RE = re.compile(r'\.(?:png|jpe?g|gif|webp|bmp|svg|avif|ico)$', re.I)


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
    """Local files referenced by a step page (link targets + <video>/<audio> src).

    Markdown pages that link to *other* step pages (`.md`) are excluded: those are
    cross-references, not assets this step produced, so they must not gate the resume
    check (a later step's page can legitimately not exist yet).
    """
    for m in list(MD_LINK_RE.finditer(md_text)) + list(MD_SRC_RE.finditer(md_text)):
        dest = m.group(1)
        if not REMOTE_DEST_RE.match(dest):
            dest = urllib.parse.unquote(dest)
            if dest.lower().endswith((".md", ".markdown")):
                continue
            yield dest


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


def build_step_link_map(items):
    """Map each step's URL (relative and absolute) to its local Markdown path.

    `items` is `collect_steps()` output: `(folder_parts, step)` tuples. The map lets us
    rewrite the FutureLearn step links that appear in step bodies (glossary keyword
    links, "see step N.M", …) so they point at the local `.md` file instead of the
    live course URL.
    """
    m = {}
    for parts, step in items:
        rel = os.path.join(*parts, f"{mf.sanitize(step.get('title', ''))}.md")
        href = step.get("href", "")
        if href:
            m[href] = rel
            if href.startswith("/"):
                m[mf.BASE_URL + href] = rel
    return m


def localize_step_links(html_text, link_map, from_dir):
    """Rewrite step links in body HTML to local relative Markdown paths.

    `from_dir` is the folder (relative to the course root) that holds the page being
    generated, so each target becomes a `../…/page.md` path valid from that folder.
    Fragment anchors (`#t`, `#japanese-history-overview`) are preserved.
    """
    def repl(m):
        quote = m.group(1)
        url = m.group(2).strip()
        frag = ""
        if "#" in url:
            url, frag = url.split("#", 1)
        if url in link_map:
            rel = os.path.relpath(link_map[url], from_dir)
            new = url_path(rel)
            if frag:
                new += "#" + frag
            return f'href={quote}{new}{quote}'
        return m.group(0)

    return re.sub(r'href\s*=\s*(["\'])([^"\']+)\1', repl, html_text, flags=re.I)


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


def _write_stream(r, dest):
    """Write a streamed response to `dest` with a progress bar sized from Content-Length
    (or indeterminate when the server omits it, as FutureLearn's redirected downloads do)."""
    total = int(r.headers.get("content-length") or 0)
    with open(dest, "wb") as fh, tqdm(total=total or None, unit="B", unit_scale=True,
                                       desc=os.path.basename(dest), leave=False) as bar:
        for chunk in r.iter_content(chunk_size=1 << 16):
            fh.write(chunk)
            bar.update(len(chunk))


def download_bytes(session, url, dest):
    r = session.get(url, timeout=120, stream=True)
    r.raise_for_status()
    _write_stream(r, dest)
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
        r = session.get(link_url, timeout=120, stream=True)
        r.raise_for_status()
        ext = os.path.splitext(r.url.split("?")[0])[1] or ".bin"
    if not ext.startswith("."):
        ext = "." + ext

    fname = unique_name(title_sane, ext, used)
    dest = os.path.join(folder, fname)
    if os.path.exists(dest) and os.path.getsize(dest) > 0:
        return fname

    if r is None:
        r = session.get(link_url, timeout=120, stream=True)
        r.raise_for_status()
    _write_stream(r, dest)
    return fname


def resolve_final_url(session, url):
    """Follow redirects and return the final URL, falling back to the input on error.

    FutureLearn's `relatedLinks[].url` values are `/links/l/<id>` short links that 302
    to the real destination. Storing the short link in the Markdown produces a dead
    link once the course is served outside FutureLearn, so we resolve it here with a
    cheap HEAD-style GET (streamed, body never read).
    """
    try:
        r = session.get(url, timeout=60, stream=True)
        final = r.url
        r.close()
        return final
    except Exception:
        return url


def resolve_related_links(session, related_links):
    """Return `relatedLinks` with every `url` rewritten to its final redirect target."""
    out = []
    for lnk in related_links:
        url = lnk.get("url", "")
        title = lnk.get("title", url or "link")
        if url.startswith("/"):
            url = mf.BASE_URL + url
        out.append({"title": title, "url": resolve_final_url(session, url)})
    return out


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


def _image_ext(url):
    """Extension (with dot) when `url` points at an image, else empty string."""
    path = urllib.parse.urlsplit(url).path
    m = IMAGE_EXT_RE.search(path)
    return m.group(0) if m else ""


def localize_images(session, body_html, folder, base, used):
    """Download inline `<img>` and image-link targets and rewrite them to local files.

    FutureLearn step bodies embed images on the partner CDN (`fl-keio.info` etc.) and
    "take a closer look" links that point straight at those images. Leaving them as
    remote URLs breaks a fully-offline copy, so each unique image is fetched once and
    every `src`/`href` that referenced it is rewritten to the local file, the same way
    inline audio clips are localised.
    """
    urls = []
    for m in IMG_TAG_SRC_RE.finditer(body_html):
        u = m.group(2).strip()
        if u and u not in urls:
            urls.append(u)
    for m in A_TAG_HREF_RE.finditer(body_html):
        u = m.group(2).strip()
        if u and _image_ext(u) and u not in urls:
            urls.append(u)

    for raw in urls:
        u = H.unescape(raw)
        ext = _image_ext(u) or ".png"
        stem = os.path.splitext(os.path.basename(urllib.parse.urlsplit(u).path))[0]
        stem = mf.sanitize(stem) if stem else f"{base}-image"
        fname = unique_name(stem, ext, used)
        dest = os.path.join(folder, fname)
        if not (os.path.exists(dest) and os.path.getsize(dest) > 0):
            try:
                download_bytes(session, u, dest)
                print(f"      image -> {fname}")
            except Exception as e:
                print(f"      ! image failed: {e}", file=sys.stderr)
                continue
        body_html = body_html.replace(raw, fname)
    return body_html


HLS_WORKERS = 8  # parallel segment connections; measured sweet spot on the CDN

# Instrumentation for benchmarks: (bytes, seconds) of the *transfer* phase of the
# most recent native video download. Lets measure.sh report net-vs-mux split.
_LAST_NET = None

# A separate curl Session per worker thread: curl_cffi Sessions are not safe to
# share across threads when each request can block, and a fresh Session per
# request would redo TLS. 8 threads x one pinned Session = 8 parallel
# connections (HTTP/2 streams), which saturates the CDN/line on this setup.
_thread_local = threading.local()


def _thread_session():
    s = getattr(_thread_local, "session", None)
    if s is None:
        s = creq.Session(impersonate="chrome124")
        _thread_local.session = s
    return s


def _hls_variant_uri(master_text):
    """Highest-bandwidth non-I-FRAME variant URI from a master playlist."""
    best = None
    for i, line in enumerate(master_text.splitlines()):
        if line.startswith("#EXT-X-STREAM-INF") and "I-FRAME" not in line:
            m = re.search(r"BANDWIDTH=(\d+)", line)
            if m and (best is None or int(m.group(1)) > best[0]):
                best = (int(m.group(1)), master_text.splitlines()[i + 1])
    return best


def _download_hls_native(master_url, dest_mp4):
    """Download all HLS segments in parallel, then mux them locally to mp4.

    ffmpeg's own HLS demuxer only opens 1-2 segment connections, so a single
    stream limps at a few MB/s even on fast lines. Fetching every segment with
    `HLS_WORKERS` parallel Sessions saturates the CDN instead, and the one
    local ffmpeg pass (concat + `-c copy`) turns the segments into the same
    mp4 the old path produced. Raises on anything it can't handle (encrypted
    or live playlists, empty segment lists, mux failure) so the caller can
    fall back to ffmpeg's own demuxer.
    """
    global _LAST_NET
    sess = creq.Session(impersonate="chrome124")
    r = sess.get(master_url, timeout=60)
    r.raise_for_status()
    master_final = str(r.url)  # after redirects; child URIs resolve against this
    best = _hls_variant_uri(r.text)
    if best is None:
        raise ValueError("no usable HLS variant in master playlist")
    child_url = urllib.parse.urljoin(master_final, best[1])
    rc = sess.get(child_url, timeout=60)
    rc.raise_for_status()
    child = rc.text
    if "#EXT-X-KEY" in child:
        raise ValueError("encrypted segments (EXT-X-KEY) — ffmpeg handles these")
    if "#EXT-X-ENDLIST" not in child:
        raise ValueError("live playlist (no ENDLIST) — ffmpeg handles these")
    seg_urls = [urllib.parse.urljoin(str(rc.url), line)
                for line in child.splitlines() if line and not line.startswith("#")]
    if not seg_urls:
        raise ValueError("segment list is empty")

    tmp = tempfile.mkdtemp(prefix="flhls-", dir=os.path.dirname(dest_mp4))
    part = dest_mp4 + ".part"
    total = [0]
    net_s = 0.0
    ok = False
    try:
        # Estimate the total size for a determinate bar from the playlist
        # itself (variant bitrate x summed durations) — cheaper than HEADing
        # every segment, and the estimate is within a few percent.
        dur = sum(float(x) for x in re.findall(r"#EXTINF:([0-9.]+)", child))
        total_hint = int(best[0] / 8 * dur) if dur else 0

        # Parallel segment download. `last`/`total` are only touched by the
        # main thread (as_completed loop), so no locking is needed. `ready[i]`
        # is signalled once segment i is fully on disk so the mux feeder can
        # consume segments in order as they arrive.
        def _dl(iu):
            i, u = iu
            res = _thread_session().get(u, timeout=120, stream=True)
            res.raise_for_status()
            n = 0
            with open(os.path.join(tmp, f"seg_{i:05d}.ts"), "wb") as fh:
                for chunk in res.iter_content(chunk_size=1 << 16):
                    fh.write(chunk)
                    n += len(chunk)
            ready[i].set()
            return n
        ready = [threading.Event() for _ in seg_urls]
        seg_paths = [os.path.join(tmp, f"seg_{i:05d}.ts") for i in range(len(seg_urls))]
        dl_failed = threading.Event()

        # Mux directly off the arriving segments: pipe each finished segment
        # into ffmpeg in playlist order. Download (2s) and mux (1s) overlap,
        # so the mux pass is almost entirely hidden behind the transfer.
        def _feed(stdin):
            try:
                for i, p in enumerate(seg_paths):
                    # poll so a failed download aborts the mux promptly
                    while not ready[i].is_set() and not dl_failed.is_set():
                        time.sleep(0.01)
                    if dl_failed.is_set():
                        return
                    with open(p, "rb") as fh:
                        while True:
                            chunk = fh.read(1 << 18)
                            if not chunk:
                                break
                            stdin.write(chunk)
            finally:
                try:
                    stdin.close()
                except Exception:
                    pass

        t0 = time.perf_counter()
        with tqdm(total=total_hint or None, unit="B", unit_scale=True,
                  desc=os.path.basename(dest_mp4), leave=False) as bar:
            cmd = ["ffmpeg", "-y", "-loglevel", "error", "-f", "mpegts", "-i", "pipe:0",
                   "-c", "copy", "-bsf:a", "aac_adtstoasc", "-movflags", "+faststart",
                   "-f", "mp4", part]
            proc = subprocess.Popen(cmd, stdin=subprocess.PIPE,
                                    stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
            # Read stderr on a side thread: Popen.communicate() would try to
            # flush+close stdin, but the feeder thread owns stdin (it closes it
            # on EOF), which makes communicate() raise. Drain stderr instead
            # so -loglevel error output can never fill the 64KB pipe.
            stderr_io = []
            def _read_err():
                stderr_io.append(proc.stderr.read())
            err_reader = threading.Thread(target=_read_err, daemon=True)
            err_reader.start()
            feeder = threading.Thread(target=_feed, args=(proc.stdin,), daemon=True)
            feeder.start()
            with concurrent.futures.ThreadPoolExecutor(max_workers=HLS_WORKERS) as ex:
                futs = [ex.submit(_dl, (i, u)) for i, u in enumerate(seg_urls)]
                try:
                    for f in concurrent.futures.as_completed(futs):
                        n = f.result()  # propagates download errors
                        total[0] += n
                        bar.update(n)
                except Exception:
                    dl_failed.set()
                    try:
                        proc.kill()
                    except Exception:
                        pass
                    raise
            feeder.join()
            proc.wait(timeout=600)
            err_reader.join(timeout=5)
            stderr = (stderr_io[0] if stderr_io else b"").decode("utf-8", "replace")
        net_s = time.perf_counter() - t0
        if proc.returncode != 0 or not (os.path.exists(part)
                                         and os.path.getsize(part) > 0):
            raise ValueError(f"ffmpeg mux failed: {stderr.strip()[:300]}")
        # Backstop: a truncated/error-muxed file must not be renamed into place.
        # ffprobe costs ~0.2s and catches streams that muxed off-bounds.
        dura = subprocess.run(
            ["ffprobe", "-v", "error", "-show_entries", "format=duration",
             "-of", "default=nw=1:nk=1", part],
            capture_output=True, text=True, timeout=30)
        try:
            got = float(dura.stdout.strip())
        except ValueError:
            got = 0.0
        if got <= 0 or (dur and abs(got - dur) / dur > 0.05):
            raise ValueError(f"muxed duration {got:.1f}s vs playlist {dur:.1f}s")
        os.replace(part, dest_mp4)
        ok = True
        return True
    finally:
        shutil.rmtree(tmp, ignore_errors=True)
        if os.path.exists(part):
            os.remove(part)
        if ok:
            _LAST_NET = (total[0], net_s)


def _download_video_ffmpeg(vid, dest_mp4):
    """Fallback: let ffmpeg's own demuxer fetch and mux the HLS stream.

    Used when the native parallel path can't handle the playlist (encrypted
    segments, live streams, unexpected structure). Slower on fast lines but
    maximally compatible. Still writes `.part` and renames on success, so an
    interrupted run never leaves a truncated file under the final name.
    """
    url = f"https://view.vzaar.com/{vid}/adaptive.m3u8"
    part = dest_mp4 + ".part"
    cmd = ["ffmpeg", "-y", "-loglevel", "error", "-i", url,
           "-c", "copy", "-bsf:a", "aac_adtstoasc", "-movflags", "+faststart",
           "-f", "mp4", part]
    proc = subprocess.Popen(cmd, stderr=subprocess.PIPE, text=True)

    # ffmpeg gives no reliable total duration for adaptive HLS up front, so the bar just
    # ticks by polling how large `.part` has grown — indeterminate, but shows liveness.
    stop = threading.Event()
    def _poll():
        with tqdm(unit="B", unit_scale=True, desc=os.path.basename(dest_mp4),
                  leave=False) as bar:
            last = 0
            while not stop.wait(0.5):
                size = os.path.getsize(part) if os.path.exists(part) else 0
                bar.update(size - last)
                last = size
    poller = threading.Thread(target=_poll, daemon=True)
    poller.start()
    stderr = proc.communicate()[1]
    stop.set()
    poller.join()

    ok = proc.returncode == 0 and os.path.exists(part) and os.path.getsize(part) > 0
    if ok:
        os.replace(part, dest_mp4)
    else:
        print(f"      ! ffmpeg failed: {stderr.strip()[:300]}", file=sys.stderr)
        if os.path.exists(part):
            os.remove(part)
    return ok


def download_video(vid, dest_mp4):
    """Mux the HLS stream to `dest_mp4`, atomically.

    Native path first: fetch the playlists, download every segment in parallel
    (see `_download_hls_native`), then one local ffmpeg pass to mux. Falls back
    to ffmpeg's own HLS demuxer (`_download_video_ffmpeg`) for streams the
    native path can't handle.

    ffmpeg is writing a growing file, so a run killed mid-video leaves a non-empty but
    truncated .mp4 — which the resume check would then read as "already downloaded" and
    skip forever. Writing to `.part` and renaming on success means the final name only
    ever exists for a complete video. `-f mp4` is required because the `.part`
    extension gives ffmpeg no format to infer.
    """
    global _LAST_NET
    _LAST_NET = None
    try:
        return _download_hls_native(f"https://view.vzaar.com/{vid}/adaptive.m3u8",
                                    dest_mp4)
    except Exception as e:
        print(f"      ! native HLS download failed ({e}); using ffmpeg instead",
              file=sys.stderr)
        return _download_video_ffmpeg(vid, dest_mp4)


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


SENTENCE_BOUNDARY_RE = re.compile(r'[.!?]\s+[A-Z0-9"“]')


def _lead_is_short(lead, max_len=140):
    """True when the lead reads as a subtitle (one short sentence) rather than an overview."""
    text = lead.strip()
    if len(text) > max_len:
        return False
    return not SENTENCE_BOUNDARY_RE.search(text)


def _blockquote(text):
    """Render a paragraph as a Markdown blockquote."""
    return "> " + text.replace("\n", "\n> ")


TITLE_QUOTE_RE = re.compile(r'["\'“”‘’《》「」『』]')
TITLE_TRAILING_RE = re.compile(r'[\s?.!:]+$')


def _normalize_heading(text):
    """Strip quoting/punctuation noise so a straight-quoted title and the same title
    rendered with curly quotes (or wrapped in a book title mark) compare as equal."""
    text = TITLE_QUOTE_RE.sub("", text)
    return TITLE_TRAILING_RE.sub("", text.strip()).casefold()


def _is_redundant_h1(lead, title):
    """True when body's own opening H1 just repeats the step title, as external articles
    (FutureLearn's `Article reading` steps) often do by carrying their original heading
    into the fetched body — printing both looks like a duplicated header."""
    if not lead.startswith("# "):
        return False
    heading = _normalize_heading(lead[2:])
    step_title = _normalize_heading(title)
    return bool(heading) and bool(step_title) and (
        heading == step_title or heading.startswith(step_title) or step_title.startswith(heading)
    )


def build_step_markdown(title, data, title_sane, sub_links, download_links,
                        audio_files=(), quiz_lines=()):
    lines = [f"# {title}", ""]
    video = data["video"]
    if video:
        lines.append(f'<video controls src="{url_path(title_sane + ".mp4")}"></video>')
        lines.append("")

    if data["body_html"]:
        body_md = body_to_markdown(data["body_html"], audio_files)
        # The first paragraph is FutureLearn's step overview: promote it to a subtitle (##)
        # only when it's a genuinely short one-liner; a longer multi-sentence summary reads
        # wrong as a heading, so render it as a blockquote instead. A paragraph carrying an
        # audio player, or a body that already opens with a heading (e.g. a poll's H2), is
        # left alone — unless that heading just repeats the title we already printed above.
        if "\n\n" in body_md and "<audio" not in body_md.split("\n\n", 1)[0]:
            lead, rest = body_md.split("\n\n", 1)
            lead = lead.strip()
            if _is_redundant_h1(lead, title):
                lines.append(rest.strip())
            elif lead.startswith("#"):
                lines.append(body_md)
            else:
                lines.append(f"## {lead}" if _lead_is_short(lead) else _blockquote(lead))
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


def course_source_url(items):
    """First step URL — a stable, re-scrapable entry point for this course.

    The course home/run URL doesn't always render the embedded course tree (some runs
    serve an overview page whose first step link points at a different run), so the
    first step URL from the tree itself is the reliable input for a later re-run.
    """
    if items:
        href = items[0][1].get("href", "")
        if href:
            return mf.BASE_URL + href
    return ""


def build_toc(root_name, items, locked=(), source_url=""):
    """Render the ToC. Locked weeks are saved as normal headings; weeks skipped via
    --skip-locked are listed as unlinked placeholders. `source_url` is recorded as
    YAML frontmatter so the course can be re-scraped without hunting down its URL."""
    scraped_weeks = {parts[0] for parts, _ in items}
    lines = []
    if source_url:
        lines += ["---", f"source: {source_url}", "---", ""]
    lines += [f"# {root_name}", ""]
    cur_week = cur_act = None
    for parts, step in items:
        week, act, stepdir = parts
        title = mf.sanitize(step.get("title", ""))
        rel = os.path.join(week, act, stepdir, f"{title}.md")
        if week != cur_week:
            lines.append(f"## {week}")
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

def scrape_one(html_text, session, args):
    """Scrape a single course from a page whose HTML embeds the course tree."""
    weeks = mf.extract_weeks(html_text)
    root_name = mf.sanitize(mf.extract_run_title(html_text))
    root = os.path.join(args.out, root_name)
    items = mf.collect_steps(weeks, include_locked=not args.skip_locked)
    locked = mf.locked_weeks(weeks)
    source_url = course_source_url(items)

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

    step_links = build_step_link_map(items)  # step URL -> local .md (rel. to course root)

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

        if data["related_links"]:
            data["related_links"] = resolve_related_links(session, data["related_links"])

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

        if data["body_html"]:
            data["body_html"] = localize_images(
                session, data["body_html"], folder, title_sane, used_names)
            data["body_html"] = localize_step_links(
                data["body_html"], step_links, os.path.join(*parts))

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
    toc = build_toc(root_name, items, locked, source_url)
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


# ---------------------------------------------------------------------------
# CLI entry point
# ---------------------------------------------------------------------------

STEP_URL_RE = re.compile(r'/courses/[A-Za-z0-9._~-]+/\d+/steps/\d+')


def read_links(path):
    """Read a file of one course URL per line, skipping blanks and # comments."""
    urls = []
    with open(path, encoding="utf-8") as fh:
        for ln in fh:
            ln = ln.strip()
            if ln and not ln.startswith("#"):
                urls.append(ln)
    return list(dict.fromkeys(urls))  # preserve order, drop exact duplicates


def has_course_tree(html_text):
    try:
        mf.extract_weeks(html_text)
        return True
    except ValueError:
        return False


def fetch_course_page(session, url):
    """Fetch a course URL and return HTML that carries the course tree.

    The tree lives in the course-content sidebar, present on a course's home and step
    pages. If the given URL doesn't render it (an overview or marketing page), follow the
    first step link found in the page instead.
    """
    html_text = fetch_text(session, url)
    if has_course_tree(html_text):
        return html_text
    m = STEP_URL_RE.search(html_text)
    if m:
        step_url = urllib.parse.urljoin(url, m.group(0))
        print(f"  (no course tree here; following {step_url})")
        return fetch_text(session, step_url)
    return html_text  # let scrape_one raise the real error


def main():
    ap = argparse.ArgumentParser(
        description="Scrape one or more FutureLearn courses to folders + markdown.")
    ap.add_argument("course_url", nargs="?",
                    help="course URL to download (single-course mode); "
                         "or pass --links FILE for several courses")
    ap.add_argument("--links", metavar="FILE",
                    help="file with one course URL per line; downloads each "
                         "(blank lines and # comments ignored)")
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

    if args.links and args.course_url:
        ap.error("give either a course URL or --links FILE, not both")
    if not args.links and not args.course_url:
        ap.error("provide a course URL, or --links FILE with one course URL per line")

    session = make_session(parse_cookies(args.cookies))

    if args.links:
        urls = read_links(args.links)
        if not urls:
            raise SystemExit(f"No course URLs found in {args.links}")
        for url in urls:
            print(f"\n=== {url} ===")
            try:
                html_text = fetch_course_page(session, url)
            except Exception as e:
                print(f"! failed to fetch {url}: {e}", file=sys.stderr)
                continue
            try:
                scrape_one(html_text, session, args)
            except ValueError as e:
                print(f"! {url}: {e}", file=sys.stderr)
                continue
    else:
        print(f"\n=== {args.course_url} ===")
        try:
            html_text = fetch_course_page(session, args.course_url)
        except Exception as e:
            raise SystemExit(f"! failed to fetch {args.course_url}: {e}")
        try:
            scrape_one(html_text, session, args)
        except ValueError as e:
            raise SystemExit(str(e))


if __name__ == "__main__":
    main()
