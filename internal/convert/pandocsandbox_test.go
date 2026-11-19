package convert

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tileforge/octoconv-worker-pandoc/internal/sandbox"
)

func TestPandocGrantsMatchTheMeasuredSet(t *testing.T) {
	if strings.Join(pandocReadOnly, "\n") != strings.Join([]string{
		"/lib",
		"/usr/share/pandoc",
		"/etc/ld.so.cache",
		"/etc/localtime",
	}, "\n") {
		t.Fatalf("read only %v", pandocReadOnly)
	}
	if strings.Join(pandocEngines, "\n") != "pandoc" {
		t.Fatalf("engines %v", pandocEngines)
	}
	if pandocPreflightOutsideBase == "/tmp" || PandocSandboxDisabledEnv != "PANDOC_SANDBOX_DISABLED" {
		t.Fatal("outside fixture or disable env")
	}
}

func TestWithSandboxEmptyHelperIsUnconfined(t *testing.T) {
	c := &PandocConverter{}
	c.WithSandbox(nil)
	if c.sandbox.confines() {
		t.Fatal("confined")
	}
	proof, err := PandocPreflight(t.Context(), c)
	if err != nil || proof.OutputBytes != 0 {
		t.Fatalf("proof %+v err %v", proof, err)
	}
}

func TestForJobGrantsTheJobDirectoryAndPandoc(t *testing.T) {
	t.Setenv("TMPDIR", "/tmp")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pandoc"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	job := filepath.Join(dir, "job")
	c := (&PandocConverter{}).WithSandbox([]string{"/usr/local/bin/pandoc-worker", sandbox.ExecMode})
	spec := strings.Join(c.jobExec(job).spec, "\n")
	for _, want := range []string{
		"rw:" + job,
		"ro:/lib",
		"ro:/usr/share/pandoc",
		"ro:/etc/ld.so.cache",
		"ro:" + filepath.Join(dir, "pandoc"),
	} {
		if !strings.Contains(spec, want) {
			t.Errorf("missing %s in %s", want, spec)
		}
	}
	jobGrant := "rw:" + job
	if jobGrant == "rw:/" || jobGrant == "rw:/tmp" || jobGrant == "rw:/tmp/" {
		t.Fatalf("job directory is a shared path: %s", job)
	}
	for _, token := range strings.Split(spec, "\n") {
		if token == jobGrant {
			continue
		}
		if token == "rw:/" || token == "rw:/tmp" || strings.HasPrefix(token, "rw:/tmp/") || token == "ro:/tmp" || token == "ro:/" {
			t.Fatalf("spec grants a shared path: %s", spec)
		}
	}
	var bare PandocConverter
	if got := bare.jobExec(job).spec; len(got) != 0 {
		t.Fatalf("unconfined spec %q", got)
	}
}

func TestPandocDeniedOutsideRejectsSetupFailureAndAWrittenMarker(t *testing.T) {
	if err := pandocDeniedOutside(ErrEngineSandboxUnavailable, "", []byte("x")); err == nil || !strings.Contains(err.Error(), "sandbox setup failed") {
		t.Fatal(err)
	}
	dir := t.TempDir()
	written := filepath.Join(dir, "neg.html")
	if err := os.WriteFile(written, []byte("OCTOCONV SANDBOX PREFLIGHT"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := pandocDeniedOutside(errors.New("permission denied"), written, []byte("OCTOCONV SANDBOX PREFLIGHT")); err == nil || !strings.Contains(err.Error(), "engine wrote") {
		t.Fatal(err)
	}
	if err := pandocDeniedOutside(errors.New("permission denied"), filepath.Join(dir, "missing.html"), []byte("OCTOCONV SANDBOX PREFLIGHT")); err != nil {
		t.Fatal(err)
	}
}
