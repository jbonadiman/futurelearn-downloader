package course

import "testing"

func TestExtractStepFromRealShape(t *testing.T) {
	data, err := ExtractStep(loadFixture(t, "step-1992614.html"))
	if err != nil {
		t.Fatalf("ExtractStep: %v", err)
	}
	if data.BodyHTML == "" {
		t.Fatal("body_html not populated")
	}
	if data.Video == nil || data.Video.VzaarID != "1000001" {
		t.Fatalf("video = %+v", data.Video)
	}
	if len(data.Video.Subtitles) != 5 {
		t.Fatalf("subtitles = %d, want 5", len(data.Video.Subtitles))
	}
	if len(data.RelatedFiles) != 1 || data.RelatedFiles[0].Type != "pdf" {
		t.Fatalf("related files = %+v", data.RelatedFiles)
	}
}

func TestExtractStepVideoWithoutSubtitlesOrDownloads(t *testing.T) {
	data, err := ExtractStep(loadFixture(t, "step-2014685.html"))
	if err != nil {
		t.Fatalf("ExtractStep: %v", err)
	}
	if data.Video == nil || data.Video.VzaarID != "1000003" {
		t.Fatalf("video = %+v", data.Video)
	}
	if len(data.Video.Subtitles) != 0 || len(data.RelatedFiles) != 0 {
		t.Fatalf("expected no subtitles/related files: %+v", data)
	}
}

func TestExtractStepNonVideoStepHasNoVideo(t *testing.T) {
	data, err := ExtractStep(loadFixture(t, "step-1992443.html"))
	if err != nil {
		t.Fatalf("ExtractStep: %v", err)
	}
	if data.Video != nil {
		t.Fatalf("video = %+v, want nil", data.Video)
	}
	if data.BodyHTML == "" {
		t.Fatal("body_html not populated")
	}
}

func TestExtractStepRelatedLinksFromSyntheticBlob(t *testing.T) {
	html := `<script type="application/json" data-hypernova-key="x"><!--
{"stepContent":{"body":{"__html":"<p>hi</p>"}},"relatedContent":{"relatedLinks":[{"title":"Extra reading","url":"/link/abc"}],"relatedFiles":[]}}
--></script>`
	data, err := ExtractStep(html)
	if err != nil {
		t.Fatalf("ExtractStep: %v", err)
	}
	if len(data.RelatedLinks) != 1 || data.RelatedLinks[0].Title != "Extra reading" || data.RelatedLinks[0].URL != "/link/abc" {
		t.Fatalf("related links = %+v", data.RelatedLinks)
	}
}
