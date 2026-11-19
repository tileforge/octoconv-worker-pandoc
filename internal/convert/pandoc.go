package convert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// ErrPandocInputTooLarge means the input exceeds the configured ceiling.
var ErrPandocInputTooLarge = errors.New("input too large")

const pandocUncompressedFactor = 4

type commandRunner func(ctx context.Context, name string, args ...string) error

// PandocConverter runs pandoc for the closed pair table.
type PandocConverter struct {
	MaxInputBytes int64
	run           commandRunner
	sandbox       *engineSandbox
}

var live = &PandocConverter{MaxInputBytes: DefaultMaxInputBytes}

func init() {
	Default.Register(live)
}

// Pairs returns the pandocPairTable pairs in the order written in that table.
func (PandocConverter) Pairs() []Pair {
	table := []struct {
		from string
		to   []string
	}{
		{"md", []string{"html", "docx", "epub", "rst", "org", "latex"}},
		{"rst", []string{"html", "docx", "epub", "md"}},
		{"org", []string{"html", "docx", "epub", "md"}},
		{"latex", []string{"html", "docx", "epub", "md"}},
		{"html", []string{"md", "docx", "epub", "rst", "latex"}},
		{"docx", []string{"md", "html", "rst", "org", "latex", "epub"}},
		{"epub", []string{"md", "html", "docx"}},
	}
	var pairs []Pair
	for _, row := range table {
		for _, to := range row.to {
			pairs = append(pairs, Pair{From: row.from, To: to})
		}
	}
	return pairs
}

func pandocPairSupported(from, to string) bool {
	for _, pair := range (PandocConverter{}).Pairs() {
		if pair.From == from && pair.To == to {
			return true
		}
	}
	return false
}

type pandocOpts struct {
	Standalone     bool   `json:"standalone,omitempty"`
	Toc            bool   `json:"toc,omitempty"`
	NumberSections bool   `json:"number_sections,omitempty"`
	Wrap           string `json:"wrap,omitempty"`
	SectionDivs    bool   `json:"section_divs,omitempty"`
}

func parsePandocOpts(opts map[string]any) (pandocOpts, error) {
	if len(opts) == 0 {
		return pandocOpts{}, nil
	}
	raw, err := json.Marshal(opts)
	if err != nil {
		return pandocOpts{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var o pandocOpts
	if err := dec.Decode(&o); err != nil {
		return pandocOpts{}, err
	}
	switch o.Wrap {
	case "", "auto", "none", "preserve":
	default:
		return pandocOpts{}, fmt.Errorf("unsupported wrap %q", o.Wrap)
	}
	return o, nil
}

func (o pandocOpts) args() []string {
	var args []string
	if o.Standalone {
		args = append(args, "--standalone")
	}
	if o.Toc {
		args = append(args, "--toc")
	}
	if o.NumberSections {
		args = append(args, "--number-sections")
	}
	if o.SectionDivs {
		args = append(args, "--section-divs")
	}
	if o.Wrap != "" {
		args = append(args, "--wrap="+o.Wrap)
	}
	return args
}

func pandocFormatName(f string) string {
	if f == "md" {
		return "markdown"
	}
	return f
}

// The latex reader rejects a raw_html suffix. rst, org, and docx have neither extension.
func pandocReaderSpec(src string) string {
	name := pandocFormatName(src)
	switch src {
	case "md", "html", "epub":
		return name + "-raw_html-raw_tex"
	case "latex":
		return "latex-raw_tex"
	default:
		return name
	}
}

func pandocStructural(tgt string) bool {
	switch tgt {
	case "html", "latex", "docx", "epub":
		return true
	default:
		return false
	}
}

func pandocConvertArgs(src, tgt, inPath, outPath string, o pandocOpts) []string {
	args := []string{"-f", pandocReaderSpec(src), "-t", pandocFormatName(tgt), "--sandbox"}
	optArgs := o.args()
	if pandocStructural(tgt) && !containsArg(optArgs, "--standalone") {
		args = append(args, "--standalone")
	}
	args = append(args, optArgs...)
	args = append(args, inPath, "-o", outPath)
	return args
}

func containsArg(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag {
			return true
		}
	}
	return false
}

// Convert runs pandoc. Unknown options fail before pandoc starts.
func (c *PandocConverter) Convert(ctx context.Context, inPath, outPath string, opts map[string]any) error {
	src := NormalizeFormat(filepath.Ext(inPath))
	tgt := NormalizeFormat(filepath.Ext(outPath))
	if !pandocPairSupported(src, tgt) {
		return fmt.Errorf("unsupported conversion %s -> %s", src, tgt)
	}
	parsed, err := parsePandocOpts(opts)
	if err != nil {
		return err
	}
	limit := c.MaxInputBytes
	if limit <= 0 {
		limit = DefaultMaxInputBytes
	}
	if err := enforcePandocInputSize(inPath, limit); err != nil {
		return err
	}
	args := pandocConvertArgs(src, tgt, inPath, outPath, parsed)
	if c.run != nil {
		if err := c.run(ctx, "pandoc", args...); err != nil {
			return err
		}
		return validatePandocOutput(tgt, outPath)
	}
	if c.sandbox.confines() {
		ex := c.jobExec(filepath.Dir(outPath))
		cmdName, cmdArgs, err := ex.sandbox.wrapEngine("pandoc", ex.spec, args)
		if err != nil {
			return err
		}
		if err := runCommandEnv(ctx, ex.env, cmdName, cmdArgs...); err != nil {
			return ex.sandbox.sandboxFault(err)
		}
		return validatePandocOutput(tgt, outPath)
	}
	if err := runCommand(ctx, "pandoc", args...); err != nil {
		return err
	}
	return validatePandocOutput(tgt, outPath)
}

func enforcePandocInputSize(inPath string, maxBytes int64) error {
	info, err := os.Stat(inPath)
	if err != nil {
		return fmt.Errorf("stat input: %w", err)
	}
	if info.Size() > maxBytes {
		return fmt.Errorf("%w: %d bytes exceeds %d", ErrPandocInputTooLarge, info.Size(), maxBytes)
	}
	f, err := os.Open(inPath)
	if err != nil {
		return fmt.Errorf("open input: %w", err)
	}
	defer f.Close()
	expanded, ok, err := declaredUncompressed(f, info.Size())
	if err != nil || !ok {
		return err
	}
	ceiling := uncompressedCeiling(maxBytes)
	if expanded > ceiling {
		return fmt.Errorf("%w: declared uncompressed %d exceeds %d", ErrPandocInputTooLarge, expanded, ceiling)
	}
	return nil
}

func uncompressedCeiling(maxBytes int64) uint64 {
	if maxBytes <= 0 {
		return 0
	}
	m := uint64(maxBytes)
	if m > math.MaxUint64/pandocUncompressedFactor {
		return math.MaxUint64
	}
	return m * pandocUncompressedFactor
}
