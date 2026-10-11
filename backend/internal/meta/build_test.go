// Package meta: test tentang repo itu sendiri (binary, dokumen), bukan tentang perilaku aplikasi.
package meta

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// symbols membangun ./cmd/ledgerpay dengan tag tertentu dan mengembalikan output `go tool nm`.
func symbols(t *testing.T, tags string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "ledgerpay")
	args := []string{"build", "-o", bin}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	args = append(args, "./cmd/ledgerpay")
	build := exec.CommandContext(t.Context(), "go", args...)
	build.Dir = moduleRoot(t)
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build -tags %q: %v\n%s", tags, err, out)
	}
	out, err := exec.CommandContext(t.Context(), "go", "tool", "nm", bin).Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// moduleRoot: folder Go module (backend/): go build, api/, internal/.
func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// repoRoot: akar repo (README, docs/, deploy/, perf/, frontend/).
func repoRoot(t *testing.T) string {
	t.Helper()
	return filepath.Dir(moduleRoot(t))
}

// TestBuild_DemoEndpointsAbsentWithoutDemoMode (docs/design/24-docker-compose.md §9): kode endpoint demo
// dan implementasi fault tidak ada di binary release. Sanity: binary -tags demo memuatnya (test bisa gagal).
func TestBuild_DemoEndpointsAbsentWithoutDemoMode(t *testing.T) {
	if testing.Short() {
		t.Skip("builds binaries")
	}
	// Variabel paket yang DIPAKAI kode demo (fungsi yang tak dipanggil dibuang linker, jadi tidak bisa dijadikan bukti).
	const demoOnly = "ledgerpay/internal/platform/faults.exit"
	// demoStory: data cerita sandbox pengunjung (POST /demo/sessions membuat user + saldo "dari udara").
	release, demo := symbols(t, ""), symbols(t, "demo")
	if strings.Contains(release, demoOnly) || strings.Contains(release, "demoPoints") || strings.Contains(release, "demoStory") {
		t.Fatal("release binary contains demo-only code")
	}
	if !strings.Contains(demo, demoOnly) || !strings.Contains(demo, "demoPoints") || !strings.Contains(demo, "demoStory") {
		t.Fatal("sanity: the demo binary should contain the demo code (otherwise this test proves nothing)")
	}
}

// TestBuild_NoTesthooksInRelease: titik hook test concurrency tidak bisa dipasang di binary release.
func TestBuild_NoTesthooksInRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("builds binaries")
	}
	// Registry hook yang dibaca Pause di build testhooks. testhook.Set sendiri dibuang linker (hanya
	// dipanggil test), jadi tidak bisa dijadikan bukti: versi pertama test ini gagal di sanity check.
	const hookRegistry = "ledgerpay/internal/platform/testhook.hooks"
	if strings.Contains(symbols(t, ""), hookRegistry) {
		t.Fatal("release binary contains the testhook registry")
	}
	if !strings.Contains(symbols(t, "testhooks"), hookRegistry) {
		t.Fatal("sanity: the testhooks binary should contain the testhook registry")
	}
}

// TestOpenAPI_MoneyFieldsUseMoneySchema (LI-11): di kontrak, setiap field uang memakai schema Money
// (string minor unit), bukan integer/number.
func TestOpenAPI_MoneyFieldsUseMoneySchema(t *testing.T) {
	spec := read(t, moduleRoot(t), "api/openapi.yaml")
	field := regexp.MustCompile(`^\s+(amount|balance|[a-z_]+_amount):\s*(.*)$`)
	checked := 0
	lines := strings.Split(spec, "\n")
	for i, l := range lines {
		m := field.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		checked++
		def := m[2]
		if def == "" && i+1 < len(lines) { // bentuk multi-baris: definisi ada di baris berikutnya
			def = lines[i+1]
		}
		if !strings.Contains(def, "schemas/Money") {
			t.Errorf("openapi.yaml:%d: money field %q does not use #/components/schemas/Money: %s", i+1, m[1], strings.TrimSpace(def))
		}
	}
	if checked < 10 {
		t.Fatalf("only %d money fields found: the scanner is probably broken", checked)
	}
}
