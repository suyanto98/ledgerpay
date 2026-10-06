package meta

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestTraceability_DocsReferenceExistingTests (docs/design/22-testing-strategy.md §5, M4): setiap nama test
// yang dirujuk dokumen LedgerPay benar-benar ada di kode. Dokumen yang menyebut test yang tidak ada adalah
// klaim palsu ("diuji oleh X") yang tidak ketahuan sampai ada yang mencarinya.
//
// Cakupan: dokumen milik repo ini (README, performance, runbook, ADR non-PayRun) dan bagian LedgerPay di
// strategi testing (katalog §4.1–4.3, invariant §5.1, baris FS-xx di §5.3). Bagian PayRun di dokumen
// desain bersama sengaja tidak dicek di repo ini. Bentuk yang dikenali di dalam backtick:
// `TestNama`, `TestNama/subtest` (dicek Nama), `TestPrefix_*` (minimal satu test dengan prefix itu).
func TestTraceability_DocsReferenceExistingTests(t *testing.T) {
	root := repoRoot(t)
	defined := definedTests(t, root)

	type ref struct{ name, file string }
	var refs []ref
	add := func(file, text string) {
		for _, m := range backtickTest.FindAllStringSubmatch(text, -1) {
			refs = append(refs, ref{m[1], file})
		}
	}
	whole := []string{"README.md", "docs/performance.md", "docs/runbook.md"}
	adrs, _ := filepath.Glob(filepath.Join(root, "docs/adr/ADR-*.md"))
	for _, a := range adrs {
		if !strings.Contains(filepath.Base(a), "ADR-P") { // ADR PayRun tidak berlaku di repo ini
			rel, _ := filepath.Rel(root, a)
			whole = append(whole, rel)
		}
	}
	for _, f := range whole {
		add(f, read(t, root, f))
	}
	strategy := "docs/design/22-testing-strategy.md"
	text := read(t, root, strategy)
	for _, heading := range []string{"### 4.1", "### 4.2", "### 4.3", "### 5.1"} {
		add(strategy+" "+heading, section(text, heading))
	}
	for _, line := range strings.Split(section(text, "### 5.3"), "\n") {
		if strings.HasPrefix(line, "| FS-") {
			add(strategy+" §5.3", line)
		}
	}
	if len(refs) < 50 {
		t.Fatalf("only %d test references found: the parser is probably broken", len(refs))
	}

	var missing []string
	for _, r := range refs {
		if !exists(defined, r.name) {
			missing = append(missing, r.file+": "+r.name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("%d documented test name(s) do not exist in the code:\n  %s", len(missing), strings.Join(dedupe(missing), "\n  "))
	}
}

var (
	backtickTest = regexp.MustCompile("`(Test[A-Za-z0-9_]+(?:\\*)?)(?:/[^`]*)?`")
	funcTest     = regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]+)\(`)
)

func definedTests(t *testing.T, root string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git" || d.Name() == "web") {
			return filepath.SkipDir
		}
		if strings.HasSuffix(path, "_test.go") {
			b, err := os.ReadFile(path) //nolint:gosec // G304: file di dalam repo
			if err != nil {
				return err
			}
			for _, m := range funcTest.FindAllStringSubmatch(string(b), -1) {
				out[m[1]] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func exists(defined map[string]bool, name string) bool {
	if prefix, ok := strings.CutSuffix(name, "*"); ok {
		for d := range defined {
			if strings.HasPrefix(d, prefix) {
				return true
			}
		}
		return false
	}
	return defined[name]
}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel)) //nolint:gosec // G304: file di dalam repo
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// section mengembalikan teks dari heading itu sampai heading berlevel sama atau lebih tinggi berikutnya.
func section(text, heading string) string {
	i := strings.Index(text, "\n"+heading)
	if i < 0 {
		return ""
	}
	rest := text[i+1:]
	level := strings.Count(strings.SplitN(heading, " ", 2)[0], "#")
	lines := strings.Split(rest, "\n")
	for n, l := range lines[1:] {
		if h := strings.SplitN(l, " ", 2)[0]; strings.Trim(h, "#") == "" && len(h) > 0 && len(h) <= level {
			return strings.Join(lines[:n+1], "\n")
		}
	}
	return rest
}

func dedupe(xs []string) []string {
	var out []string
	for i, x := range xs {
		if i == 0 || x != xs[i-1] {
			out = append(out, x)
		}
	}
	return out
}
