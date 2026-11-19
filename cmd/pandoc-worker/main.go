// Command pandoc-worker runs one approved pandoc capability over Worker Protocol v2.
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tileforge/octoconv-worker-pandoc/internal/convert"
	"github.com/tileforge/octoconv-worker-pandoc/internal/sandbox"
)

func main() {
	if sandbox.RunExecModeIfRequested(os.Args) {
		return
	}
	if err := refuseLegacyStores(); err != nil {
		log.Fatalf("pandoc protocol: %v", err)
	}
	applyPandocLimits()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	sandboxHelper, err := sandbox.ResolveHelper(convert.PandocSandboxDisabledEnv)
	if err != nil {
		log.Fatalf("pandoc sandbox: %v", err)
	}
	conv := convert.Current().WithSandbox(sandboxHelper)
	convert.Default.Register(conv)
	if len(sandboxHelper) == 0 {
		log.Printf("%s=true: the engine can read every file in this container", convert.PandocSandboxDisabledEnv)
	} else {
		log.Printf("pandoc sandbox: landlock via %s", strings.Join(sandboxHelper, " "))
		if err := convert.StabilizeTempDir(); err != nil {
			log.Fatalf("pandoc sandbox: %v", err)
		}
	}

	preflightCtx, cancelPreflight := context.WithTimeout(ctx, 60*time.Second)
	proof, err := convert.PandocPreflight(preflightCtx, conv)
	cancelPreflight()
	if err != nil {
		log.Fatalf("pandoc sandbox preflight: %v", err)
	}
	if len(sandboxHelper) > 0 {
		log.Printf("pandoc sandbox proven: pandoc ran inside the job directory and was denied a file outside every grant (output %d bytes, no outside output)", proof.OutputBytes)
	}

	dial, err := loadProtocolDial()
	if err != nil {
		log.Fatalf("pandoc protocol: %v", err)
	}
	log.Printf("pandoc protocol worker starting")
	if err := runProtocol(ctx, dial); err != nil {
		log.Fatalf("pandoc protocol: %v", err)
	}
}

func refuseLegacyStores() error {
	if os.Getenv("DATABASE_URL") != "" || os.Getenv("REDIS_ADDR") != "" {
		return errors.New("DATABASE_URL and REDIS_ADDR are not used")
	}
	return nil
}

func applyPandocLimits() {
	convert.Configure(envInt64("PANDOC_MAX_INPUT_BYTES", convert.DefaultMaxInputBytes))
}

func pandocFreeSlots() (int, error) {
	raw := os.Getenv("PANDOC_WORKER_CONCURRENCY")
	if raw == "" {
		return 1, nil
	}
	n, err := strconv.Atoi(firstField(raw))
	if err != nil || n != 1 {
		return 0, errors.New("PANDOC_WORKER_CONCURRENCY must be 1")
	}
	return 1, nil
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(firstField(v)); err == nil {
			return d
		}
	}
	return def
}

func envInt64(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(firstField(v), 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func firstField(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' {
			return s[:i]
		}
	}
	return s
}
