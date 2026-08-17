import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "src"))

from futurelearn_downloader import scrape as s


class FakeResp:
    def __init__(self, url, data=b"img-bytes", headers=None):
        self.url = url
        self._data = data
        self.headers = headers or {}

    def raise_for_status(self):
        pass

    def iter_content(self, chunk_size=65536):
        yield self._data

    def close(self):
        pass


class FakeSession:
    def __init__(self, final_urls=None):
        self.final_urls = final_urls or {}

    def get(self, url, **kwargs):
        return FakeResp(self.final_urls.get(url, url))


class LocalizeImagesTests(unittest.TestCase):
    def test_rewrites_img_src_and_image_links_to_local_files(self):
        with tempfile.TemporaryDirectory() as d:
            used = set()
            body = (
                '<p><img src="https://cdn.example/timeline_week2.png" alt="Timeline"></p>'
                '<p><a href="https://cdn.example/timeline_week2.png">Take a closer look</a></p>'
                '<p><img src="https://cdn.example/change_lang.jpg"></p>'
            )
            out = s.localize_images(FakeSession(), body, d, "Welcome", used)
            self.assertIn('src="timeline_week2.png"', out)
            self.assertIn('href="timeline_week2.png"', out)
            self.assertIn('src="change_lang.jpg"', out)
            self.assertNotIn("cdn.example", out)
            self.assertTrue(os.path.exists(os.path.join(d, "timeline_week2.png")))
            self.assertTrue(os.path.exists(os.path.join(d, "change_lang.jpg")))
            self.assertIn("timeline_week2.png", used)
            self.assertIn("change_lang.jpg", used)

    def test_non_image_links_are_untouched(self):
        used = set()
        body = '<p><a href="https://www.futurelearn.com/profiles/123">Profile</a></p>'
        out = s.localize_images(FakeSession(), body, "/tmp", "Welcome", used)
        self.assertEqual(out, body)
        self.assertEqual(used, set())


class ResolveRelatedLinksTests(unittest.TestCase):
    def test_short_links_follow_redirects(self):
        links = [
            {"title": "Bookshelf", "url": "/links/l/abc"},
            {"title": "Other", "url": "https://example.com/direct"},
        ]
        finals = {
            "https://www.futurelearn.com/links/l/abc": "https://www.fl-keio.info/fl_img/course03/",
        }
        out = s.resolve_related_links(FakeSession(finals), links)
        self.assertEqual(out[0]["url"], "https://www.fl-keio.info/fl_img/course03/")
        self.assertEqual(out[1]["url"], "https://example.com/direct")


class StepLinkTests(unittest.TestCase):
    def test_build_step_link_map_registers_relative_and_absolute(self):
        items = [
            (["Week 1 - X", "1. Intro", "1.1 Welcome"], {"title": "Welcome", "href": "/courses/a/5/steps/1"}),
            (["Week 1 - X", "1. Intro", "1.4 Glossary"], {"title": "Glossary of Week 1", "href": "/courses/a/5/steps/4"}),
        ]
        m = s.build_step_link_map(items)
        self.assertEqual(m["/courses/a/5/steps/4"],
                         os.path.join("Week 1 - X", "1. Intro", "1.4 Glossary", "Glossary of Week 1.md"))
        self.assertEqual(m["https://www.futurelearn.com/courses/a/5/steps/4"],
                         m["/courses/a/5/steps/4"])

    def test_localize_step_links_preserves_fragment(self):
        link_map = {
            "https://www.futurelearn.com/courses/a/5/steps/4":
                os.path.join("Week 1 - X", "1. Intro", "1.4 Glossary", "Glossary of Week 1.md"),
        }
        from_dir = os.path.join("Week 1 - X", "1. Intro", "1.1 Welcome")
        body = '<a href="https://www.futurelearn.com/courses/a/5/steps/4#k">Kenninji</a>'
        out = s.localize_step_links(body, link_map, from_dir)
        self.assertIn('href="../1.4%20Glossary/Glossary%20of%20Week%201.md#k"', out)
        self.assertNotIn("futurelearn.com", out)

    def test_localize_step_links_leaves_unknown_links(self):
        body = '<a href="https://www.futurelearn.com/profiles/99">Someone</a>'
        self.assertEqual(s.localize_step_links(body, {}, "Week 1"), body)


class LocalRefsTests(unittest.TestCase):
    def test_markdown_cross_links_are_not_treated_as_assets(self):
        md = (
            "![pic](timeline_week2.png)\n"
            "<video controls src=\"Welcome.mp4\"></video>\n"
            "[see](Week%201/1.4%20Glossary.md)\n"
            "[remote](https://example.com/x)\n"
            "[anchor](#a)\n"
        )
        refs = list(s.local_refs(md))
        self.assertIn("timeline_week2.png", refs)
        self.assertIn("Welcome.mp4", refs)
        self.assertNotIn("Week 1/1.4 Glossary.md", refs)
        self.assertNotIn("https://example.com/x", refs)
        self.assertNotIn("#a", refs)


if __name__ == "__main__":
    unittest.main()
