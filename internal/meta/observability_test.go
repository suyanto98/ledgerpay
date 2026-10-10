package meta

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestObservability_AlertsLinkToRunbook (port dari PayRun S10, skeleton 0.0.12): setiap alert punya annotation
// runbook yang menunjuk ke heading yang ADA di docs/runbook.md. Link mati ditemukan saat alert menyala jam 3
// pagi; di sini lebih murah.
func TestObservability_AlertsLinkToRunbook(t *testing.T) {
	root := repoRoot(t)
	book := read(t, root, "docs/runbook.md")
	anchors := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^#{2,4} (.+)$`).FindAllStringSubmatch(book, -1) {
		a := strings.ToLower(strings.TrimSpace(m[1]))
		anchors[regexp.MustCompile(`[^a-z0-9 -]`).ReplaceAllString(strings.ReplaceAll(a, " ", "-"), "")] = true
	}
	rules, _ := filepath.Glob(filepath.Join(root, "deploy/prometheus/rules/*.yml"))
	alertRe := regexp.MustCompile(`(?m)^\s*- alert: (\w+)$`)
	linkRe := regexp.MustCompile(`runbook: "docs/runbook\.md#([a-z0-9-]+)"`)
	checked := 0
	for _, f := range rules {
		b, err := os.ReadFile(f) //nolint:gosec // G304: file di dalam repo
		if err != nil {
			t.Fatal(err)
		}
		idx := alertRe.FindAllStringSubmatchIndex(string(b), -1)
		for i, m := range idx {
			end := len(b)
			if i+1 < len(idx) {
				end = idx[i+1][0]
			}
			name, block := string(b[m[2]:m[3]]), string(b[m[0]:end])
			link := linkRe.FindStringSubmatch(block)
			switch {
			case link == nil:
				t.Errorf("%s: alert %s has no runbook annotation pointing to docs/runbook.md", filepath.Base(f), name)
			case !anchors[link[1]]:
				t.Errorf("%s: alert %s links to docs/runbook.md#%s, which has no such heading", filepath.Base(f), name, link[1])
			}
			checked++
		}
	}
	if checked < 15 {
		t.Fatalf("expected at least 15 alerts, found %d", checked)
	}
}
