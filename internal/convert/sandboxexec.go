package convert

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"

	"github.com/tileforge/octoconv-worker-pandoc/internal/sandbox"
)

// engineSandbox is nil when the converter runs the engine directly.
// Landlock restricts the calling thread and is inherited across execve, so a
// non-nil sandbox re-execs this binary as the helper and the helper becomes
// the engine. An empty ruleset denies every path, so Confine false passes no
// path tokens.
type engineSandbox struct {
	Helper  []string
	Confine bool
}

func (s *engineSandbox) confines() bool { return s != nil && s.Confine }

func newEngineSandbox(helper []string) *engineSandbox {
	if len(helper) == 0 {
		return nil
	}
	return &engineSandbox{Helper: helper, Confine: true}
}

// wrapEngine resolves the engine in this process. The helper then execs that
// absolute path: a bare name would be looked up again after the ruleset, and
// a missing binary would exit 78.
func (s *engineSandbox) wrapEngine(name string, spec, args []string) (string, []string, error) {
	enginePath, err := exec.LookPath(name)
	if err != nil {
		return "", nil, fmt.Errorf("locate engine %s: %w", name, err)
	}
	if s == nil || len(s.Helper) == 0 {
		return enginePath, args, nil
	}
	wrapped := make([]string, 0, len(s.Helper)-1+len(spec)+2+len(args))
	wrapped = append(wrapped, s.Helper[1:]...)
	wrapped = append(wrapped, spec...)
	wrapped = append(wrapped, "--", enginePath)
	wrapped = append(wrapped, args...)
	return s.Helper[0], wrapped, nil
}

// ErrEngineSandboxUnavailable means the helper exited 78 and the engine never ran.
var ErrEngineSandboxUnavailable = errors.New("engine sandbox could not be established")

type engineExec struct {
	sandbox *engineSandbox
	spec    []string
	env     []string
}

func (e engineExec) withEnv(env []string) engineExec {
	e.env = append([]string{}, env...)
	return e
}

const sharedTmpDir = "/tmp"

func (s *engineSandbox) forJobRW(jobDir string, extraRW, readOnly, engines []string) engineExec {
	if !s.confines() {
		return engineExec{sandbox: s}
	}
	rw := append([]string{jobDir}, extraRW...)
	ro := append([]string{}, readOnly...)
	for _, name := range engines {
		if p, err := exec.LookPath(name); err == nil {
			ro = append(ro, p)
		}
	}
	return engineExec{sandbox: s, spec: sandbox.Paths{ReadWrite: rw, ReadOnly: ro}.Spec()}
}

func (e engineExec) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmdName, cmdArgs, err := e.sandbox.wrapEngine(name, e.spec, args)
	if err != nil {
		return nil, err
	}
	err = runCommandEnv(ctx, e.env, cmdName, cmdArgs...)
	return nil, e.sandbox.sandboxFault(err)
}

func (s *engineSandbox) sandboxFault(err error) error {
	if err == nil || s == nil || len(s.Helper) == 0 {
		return err
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == sandbox.SetupExit {
		return fmt.Errorf("%w: %w", ErrEngineSandboxUnavailable, err)
	}
	return err
}

// sharedTmpGuard refuses a confined ruleset that grants /tmp unless exactly one
// task runs: every task can read and sweep that directory.
func sharedTmpGuard(readWrite []string, s *engineSandbox, concurrency int, concurrencyEnv string) error {
	if !s.confines() || !slices.Contains(readWrite, sharedTmpDir) || concurrency == 1 {
		return nil
	}
	return fmt.Errorf("%s=%d with the sandbox enabled: the ruleset grants %s read-write and each task sweeps it; set %s=1",
		concurrencyEnv, concurrency, sharedTmpDir, concurrencyEnv)
}
