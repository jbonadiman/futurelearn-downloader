import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "src"))

from futurelearn_downloader.scrape import build_step_markdown


def _step(title, title_sane, body_html):
    data = {
        "body_html": body_html,
        "copyright": "",
        "related_files": [],
        "related_links": [],
        "video": None,
    }
    return build_step_markdown(title, data, title_sane, sub_links=(), download_links=())


class DuplicateTitleHeadingTests(unittest.TestCase):
    def test_body_opening_h1_matching_title_is_not_duplicated(self):
        md = _step("Are Video Games Art?", "Are Video Games Art",
                   "<h1>Are Video Games Art?</h1><p><strong>By Aaron Smuts</strong></p>")
        self.assertEqual(md.count("# Are Video Games Art?"), 1)

    def test_body_opening_h1_with_extra_subtitle_is_not_duplicated(self):
        md = _step("Game Engines", "Game Engines",
                   "<h1>Game Engines in Scientific Research</h1><p>Intro.</p>")
        self.assertEqual(md.count("# Game Engines"), 1)
        self.assertIn("Intro.", md)

    def test_unrelated_opening_h1_is_kept(self):
        data = {
            "body_html": "<h1>Wo de zhou mo</h1><p>Content.</p>",
            "copyright": "",
            "related_files": [],
            "related_links": [],
            "video": None,
        }
        md = build_step_markdown("Discuss and share with us", data, "Discuss and share with us",
                                 sub_links=(), download_links=())
        self.assertIn("# Discuss and share with us", md)
        self.assertIn("# Wo de zhou mo", md)

    def test_body_opening_h1_with_curly_quotes_is_not_duplicated(self):
        md = _step('"Black Myth: Wukong": How Did One Game Become a Global Sensation? ',
                   "Black Myth - Wukong",
                   u"<h1>\u201cBlack Myth: Wukong\u201d: How Did One Game Become a Global Sensation?</h1><p>Text.</p>")
        self.assertEqual(md.count("Black Myth"), 1)

    def test_unrelated_h2_lead_is_kept(self):
        data = {
            "body_html": "<h2>Poll question</h2><p>Content.</p>",
            "copyright": "",
            "related_files": [],
            "related_links": [],
            "video": None,
        }
        md = build_step_markdown("A step", data, "A step",
                                 sub_links=(), download_links=())
        self.assertIn("## Poll question", md)


if __name__ == "__main__":
    unittest.main()
