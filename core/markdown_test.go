package core

import (
	"strings"
	"testing"
)

func TestStripMarkdown_Table(t *testing.T) {
	input := `| 項目 | 狀態 |
|---|---|
| Phase 0-6 | ✅ |
| 流量評分 | 88/100 |`
	got := StripMarkdown(input)
	for _, want := range []string{"項目 · 狀態", "Phase 0-6 · ✅", "流量評分 · 88/100"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, banned := range []string{"|---|", "| 項目 |", "| Phase"} {
		if strings.Contains(got, banned) {
			t.Errorf("found raw markdown %q in:\n%s", banned, got)
		}
	}
}

func TestStripMarkdown_Basics(t *testing.T) {
	cases := map[string]string{
		"**bold**":          "bold",
		"*italic*":          "italic",
		"`code`":            "code",
		"# heading":         "heading",
		"[link](http://x)":  "link (http://x)",
		"~~strike~~":        "strike",
	}
	for in, want := range cases {
		if got := StripMarkdown(in); got != want {
			t.Errorf("StripMarkdown(%q) = %q, want %q", in, got, want)
		}
	}
}
