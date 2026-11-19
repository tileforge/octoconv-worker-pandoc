package convert

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Measured read-only set for pandoc. The job directory is the only writable path.
var pandocReadOnly = []string{
	"/lib",
	"/usr/share/pandoc",
	"/etc/ld.so.cache",
	"/etc/localtime",
}

var pandocEngines = []string{"pandoc"}

const (
	PandocSandboxDisabledEnv   = "PANDOC_SANDBOX_DISABLED"
	pandocPreflightOutsideBase = "/var/tmp"
	pandocPreflightText        = "OCTOCONV SANDBOX PREFLIGHT"
)

func (c *PandocConverter) WithSandbox(helper []string) *PandocConverter {
	c.sandbox = newEngineSandbox(helper)
	return c
}

func Current() *PandocConverter { return live }

func (c *PandocConverter) jobExec(workDir string) engineExec {
	if resolved, err := filepath.EvalSymlinks(workDir); err == nil {
		workDir = resolved
	}
	return c.sandbox.forJobRW(workDir, nil, pandocReadOnly, pandocEngines).withEnv(pandocEngineEnv(workDir))
}

// PandocProof is what PandocPreflight observed inside the job directory.
type PandocProof struct {
	OutputBytes int
}

func PandocPreflight(ctx context.Context, c *PandocConverter) (PandocProof, error) {
	if c == nil || !c.sandbox.confines() {
		return PandocProof{}, nil
	}
	w, cleanup, err := newPreflightWorkspaceOutside("pandoc", pandocPreflightOutsideBase)
	if err != nil {
		return PandocProof{}, err
	}
	defer cleanup()
	grants := append(append([]string{}, pandocReadOnly...), pandocEnginePaths()...)
	if err := w.requireOutsideGrants(grants); err != nil {
		return PandocProof{}, err
	}

	inside, outside, err := w.place("in.md", []byte(pandocPreflightText+"\n"))
	if err != nil {
		return PandocProof{}, err
	}
	out := filepath.Join(w.jobDir, "out.html")
	if err := c.Convert(ctx, inside, out, nil); err != nil {
		return PandocProof{}, fmt.Errorf("inside route: %w", err)
	}
	html, err := os.ReadFile(out)
	if err != nil {
		return PandocProof{}, fmt.Errorf("inside route: %w", err)
	}
	if !bytes.Contains(html, []byte(pandocPreflightText)) {
		return PandocProof{}, fmt.Errorf("inside route: output does not contain the fixture text")
	}

	negOut := filepath.Join(w.jobDir, "neg.html")
	convErr := c.Convert(ctx, outside, negOut, nil)
	if err := pandocDeniedOutside(convErr, negOut, []byte(pandocPreflightText)); err != nil {
		return PandocProof{}, fmt.Errorf("read a file outside the job directory: %w", err)
	}
	return PandocProof{OutputBytes: len(html)}, nil
}

func pandocDeniedOutside(convErr error, outPath string, marker []byte) error {
	if convErr == nil {
		return fmt.Errorf("engine accepted the outside file")
	}
	if errors.Is(convErr, ErrEngineSandboxUnavailable) {
		return fmt.Errorf("sandbox setup failed: %w", convErr)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		return nil
	}
	if bytes.Contains(data, marker) {
		return fmt.Errorf("engine wrote the outside file")
	}
	return nil
}

func pandocEnginePaths() []string {
	var paths []string
	for _, name := range pandocEngines {
		if p, err := exec.LookPath(name); err == nil {
			paths = append(paths, p)
		}
	}
	return paths
}

func pandocEngineEnv(tmpDir string) []string {
	keep := []string{"PATH", "HOME", "LANG", "LC_ALL"}
	env := make([]string, 0, len(keep)+1)
	for _, key := range keep {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	if tmpDir != "" {
		env = append(env, "TMPDIR="+tmpDir)
	}
	return env
}
