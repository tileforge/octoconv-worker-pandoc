// Package convert turns one markup file into another with the pandoc CLI.
package convert

import (
	"context"
	"errors"
	"strings"
	"time"
)

// ErrOutputTooLarge means the result exceeds the output ceiling.
var ErrOutputTooLarge = errors.New("output too large")

const (
	EnginePandoc               = "pandoc"
	OperationConvert           = "convert"
	DefaultEngineTimeout       = 65 * time.Second
	MaxFileBytes         int64 = 100 << 20
	// DefaultMaxInputBytes is the deployed PANDOC_MAX_INPUT_BYTES value.
	DefaultMaxInputBytes int64 = 3145728
)

// Pair is one source format and one target format.
type Pair struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Converter converts the file at inPath into outPath.
type Converter interface {
	Pairs() []Pair
	Convert(ctx context.Context, inPath, outPath string, opts map[string]any) error
}

// MIMEType is the content type the core stores for format.
func MIMEType(format string) string {
	switch NormalizeFormat(format) {
	case "html":
		return "text/html"
	case "md":
		return "text/markdown"
	case "rst":
		return "text/x-rst"
	case "org":
		// org-mode has no registered IANA media type.
		return "text/plain"
	case "latex":
		return "application/x-latex"
	case "docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case "epub":
		return "application/epub+zip"
	default:
		return "application/octet-stream"
	}
}

// NormalizeFormat lowercases a format or extension and strips one leading dot.
func NormalizeFormat(f string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(f), "."))
}

type opKey struct {
	Operation string
	From      string
	To        string
}

// Registry maps an operation and a format pair to a converter.
type Registry struct {
	m map[opKey]Converter
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{m: make(map[opKey]Converter)}
}

// Register indexes each pair c reports.
func (r *Registry) Register(c Converter) {
	for _, pair := range c.Pairs() {
		r.m[opKey{
			Operation: OperationConvert,
			From:      NormalizeFormat(pair.From),
			To:        NormalizeFormat(pair.To),
		}] = c
	}
}

// LookupOp reports the converter for op, from, and to.
func (r *Registry) LookupOp(op, from, to string) (Converter, bool) {
	c, ok := r.m[opKey{
		Operation: op,
		From:      NormalizeFormat(from),
		To:        NormalizeFormat(to),
	}]
	return c, ok
}

// Default is the process registry.
var Default = NewRegistry()

// Configure sets the input ceiling on the registered converter.
// A non-positive value keeps DefaultMaxInputBytes.
func Configure(maxInput int64) {
	if maxInput <= 0 {
		maxInput = DefaultMaxInputBytes
	}
	live.MaxInputBytes = maxInput
}
