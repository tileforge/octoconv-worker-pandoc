package main

import (
	"testing"

	"github.com/tileforge/octoconv-worker-pandoc/internal/convert"
)

func TestApplyPandocLimits(t *testing.T) {
	registered, ok := convert.Default.LookupOp(convert.OperationConvert, "md", "html")
	if !ok {
		t.Fatal("md to html")
	}
	live, ok := registered.(*convert.PandocConverter)
	if !ok {
		t.Fatalf("%T", registered)
	}
	before := live.MaxInputBytes
	t.Cleanup(func() { convert.Configure(before) })

	t.Setenv("PANDOC_MAX_INPUT_BYTES", "")
	applyPandocLimits()
	if live.MaxInputBytes != convert.DefaultMaxInputBytes {
		t.Fatalf("default %d", live.MaxInputBytes)
	}
	t.Setenv("PANDOC_MAX_INPUT_BYTES", "100 # bytes")
	applyPandocLimits()
	if live.MaxInputBytes != 100 {
		t.Fatalf("override %d", live.MaxInputBytes)
	}
	t.Setenv("PANDOC_MAX_INPUT_BYTES", "0")
	applyPandocLimits()
	if live.MaxInputBytes != convert.DefaultMaxInputBytes {
		t.Fatalf("zero %d", live.MaxInputBytes)
	}
}
