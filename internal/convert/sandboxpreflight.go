package convert

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type preflightWorkspace struct {
	jobDir     string
	outsideDir string
}

func newPreflightWorkspaceOutside(class, outsideBase string) (preflightWorkspace, func(), error) {
	jobDir, err := MakeTempDir("", class+"-preflight-job-")
	if err != nil {
		return preflightWorkspace{}, nil, fmt.Errorf("preflight job directory: %w", err)
	}
	outsideDir, err := MakeTempDir(outsideBase, class+"-preflight-outside-")
	if err != nil {
		os.RemoveAll(jobDir)
		return preflightWorkspace{}, nil, fmt.Errorf("preflight outside directory: %w", err)
	}
	w := preflightWorkspace{jobDir: jobDir, outsideDir: outsideDir}
	cleanup := func() {
		os.RemoveAll(jobDir)
		os.RemoveAll(outsideDir)
	}
	return w, cleanup, nil
}

// Landlock restricts the resolved path, so both sides are evaluated through
// symlinks before the comparison.
func (w preflightWorkspace) requireOutsideGrants(granted ...[]string) error {
	outside := resolveOrClean(w.outsideDir)
	for _, set := range granted {
		for _, g := range set {
			gr := resolveOrClean(g)
			if outside == gr || strings.HasPrefix(outside, gr+string(filepath.Separator)) {
				return fmt.Errorf("preflight outside directory %s lies at or beneath granted path %s", w.outsideDir, g)
			}
		}
	}
	return nil
}

func resolveOrClean(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

func pathUnder(path, root string) (bool, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(resolveExisting(absRoot), resolveExisting(absPath))
	if err != nil {
		return false, err
	}
	if rel == "." {
		return true, nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false, nil
	}
	return true, nil
}

func canonicalPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// MakeTempDir creates a directory and returns its resolved path.
// MkdirTemp keeps a TMPDIR symlink in the returned path. The sweep deletes
// that symlink when it lies inside /tmp, so the caller must keep this path.
func MakeTempDir(base, pattern string) (string, error) {
	if base == "" {
		if err := StabilizeTempDir(); err != nil {
			return "", err
		}
	}
	dir, err := os.MkdirTemp(base, pattern)
	if err != nil {
		return "", err
	}
	resolved, err := canonicalPath(dir)
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return resolved, nil
}

// RetainResolvedPath points name at the file its value resolves to.
// A symlink under /tmp is swept even when the file it names is outside /tmp,
// and a later open of the original path then fails.
func RetainResolvedPath(name string) error {
	raw, ok := os.LookupEnv(name)
	if !ok || raw == "" {
		return nil
	}
	resolved, err := canonicalPath(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if resolved == raw {
		return nil
	}
	return os.Setenv(name, resolved)
}

// StabilizeTempDir points TMPDIR at the directory it resolves to.
// os.MkdirTemp reads TMPDIR on every call. The sweep can remove a symlink
// inside /tmp that the variable still names, and the next call then fails.
func StabilizeTempDir() error {
	raw, ok := os.LookupEnv("TMPDIR")
	if !ok || raw == "" {
		return nil
	}
	resolved, err := canonicalPath(raw)
	if err != nil {
		return fmt.Errorf("TMPDIR: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("TMPDIR: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("TMPDIR %s is not a directory", raw)
	}
	if resolved == raw {
		return nil
	}
	return os.Setenv("TMPDIR", resolved)
}

func resolveExisting(p string) string {
	p = filepath.Clean(p)
	var suffix []string
	for {
		if _, err := os.Lstat(p); err == nil {
			resolved := p
			if r, err := filepath.EvalSymlinks(p); err == nil {
				resolved = r
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		suffix = append(suffix, filepath.Base(p))
		p = parent
	}
}

func (w preflightWorkspace) place(name string, data []byte) (inside, outside string, err error) {
	inside = filepath.Join(w.jobDir, name)
	outside = filepath.Join(w.outsideDir, name)
	if err := os.WriteFile(inside, data, 0o600); err != nil {
		return "", "", fmt.Errorf("preflight fixture (inside): %w", err)
	}
	if err := os.WriteFile(outside, data, 0o600); err != nil {
		return "", "", fmt.Errorf("preflight fixture (outside): %w", err)
	}
	got, err := os.ReadFile(outside)
	if err != nil {
		return "", "", fmt.Errorf("preflight canary: the outside fixture cannot be read before any ruleset: %w", err)
	}
	if !bytes.Equal(got, data) {
		return "", "", fmt.Errorf("preflight canary: the outside fixture read back different bytes")
	}
	return inside, outside, nil
}

func requireOutput(path string, want []byte) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("no output at %s: %w", path, err)
	}
	if len(data) == 0 {
		return fmt.Errorf("output at %s is empty", path)
	}
	if want != nil && !bytes.Contains(data, want) {
		return fmt.Errorf("output at %s does not contain the expected marker", path)
	}
	return nil
}

var errDeniedPDFMissing = errors.New("no pdf")

func requireDeniedPDF(path string, marker []byte) (int, error) {
	if len(marker) == 0 {
		return 0, fmt.Errorf("no marker given")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("%w at %s: a missing file does not show a denial: %w", errDeniedPDFMissing, path, err)
	}
	if len(data) == 0 || !bytes.HasPrefix(data, []byte("%PDF-")) {
		return 0, fmt.Errorf("output at %s is not a pdf", path)
	}
	if bytes.Contains(data, marker) {
		return 0, fmt.Errorf("output at %s carries the marker from the file outside every grant", path)
	}
	return len(data), nil
}

func preflightMarker() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("preflight marker: %w", err)
	}
	return "octoconv-preflight-" + hex.EncodeToString(b), nil
}
