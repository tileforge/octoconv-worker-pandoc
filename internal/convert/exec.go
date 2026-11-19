package convert

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

const (
	engineWaitDelay   = time.Second
	groupKillInterval = time.Millisecond
	groupKillBudget   = time.Second
	engineOutputLimit = 8 << 10
)

// killGroup signals the process group. Pandoc can leave a child behind after the parent exits.
func killGroup(pgid int, done <-chan error) {
	stop := time.Now().Add(groupKillBudget)
	for {
		started := time.Now()
		if err := syscall.Kill(-pgid, syscall.SIGKILL); errors.Is(err, syscall.ESRCH) || started.After(stop) {
			break
		}
		select {
		case <-done:
			done = nil
		case <-time.After(groupKillInterval):
		}
	}
	if done != nil {
		<-done
	}
}

type tailBuffer struct {
	max     int
	buf     []byte
	dropped int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) >= t.max {
		t.dropped += len(t.buf) + len(p) - t.max
		t.buf = append(t.buf[:0], p[len(p)-t.max:]...)
		return n, nil
	}
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.dropped += over
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return n, nil
}

func (t *tailBuffer) String() string {
	if t.dropped == 0 {
		return string(t.buf)
	}
	return fmt.Sprintf("[octoconv: %d earlier bytes of engine output discarded]\n%s", t.dropped, t.buf)
}

func runCommand(ctx context.Context, name string, args ...string) error {
	return runCommandEnv(ctx, nil, name, args...)
}

func runCommandEnv(ctx context.Context, env []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if env != nil {
		cmd.Env = env
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = engineWaitDelay
	stdout := &tailBuffer{max: 1 << 20}
	stderr := &tailBuffer{max: engineOutputLimit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-ctx.Done():
		killGroup(cmd.Process.Pid, done)
		return fmt.Errorf("%s killed: %w", name, ctx.Err())
	case err := <-done:
		if err != nil {
			killGroup(cmd.Process.Pid, nil)
			return fmt.Errorf("%s failed: %w: %s", name, err, stderr.String())
		}
		return nil
	}
}
