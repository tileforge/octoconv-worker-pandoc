package convert

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestSniffCapSplitsARune(t *testing.T) {
	cases := []struct {
		name   string
		target string
		prefix string
		rune   string
	}{
		{name: "markdown e acute", target: "md", prefix: "a", rune: "é"},
		{name: "markdown ellipsis", target: "md", prefix: "a", rune: "…"},
		{name: "html", target: "html", prefix: "<!doctype html>\n", rune: "…"},
		{name: "latex", target: "latex", prefix: "\\documentclass{article}\n", rune: "…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := bytes.Repeat([]byte("a"), sniffCap-1-len(tc.prefix))
			var buf bytes.Buffer
			buf.WriteString(tc.prefix)
			buf.Write(body)
			buf.WriteString(tc.rune)
			buf.WriteString(" tail\n")
			if err := validatePandocOutput(tc.target, writeBody(t, buf.Bytes())); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIllegalByteAtTheCapStillFails(t *testing.T) {
	prefix := "a"
	var buf bytes.Buffer
	buf.WriteString(prefix)
	buf.Write(bytes.Repeat([]byte("a"), sniffCap-1-len(prefix)))
	buf.WriteByte(0xFF)
	buf.WriteString(" tail\n")
	if err := validatePandocOutput("md", writeBody(t, buf.Bytes())); err == nil {
		t.Fatal("accepted 0xFF at the cap")
	}
}

func TestDanglingLeadAtEOFStillFails(t *testing.T) {
	for _, tc := range []struct {
		target string
		body   []byte
	}{
		{"md", append([]byte("plain "), 0xE2)},
		{"html", append([]byte("<!doctype html><html>"), 0xE2)},
		{"latex", append([]byte("\\documentclass{article}"), 0xE2)},
	} {
		if err := validatePandocOutput(tc.target, writeBody(t, tc.body)); err == nil {
			t.Fatalf("%s accepted a dangling lead byte", tc.target)
		}
	}
}

func writeBody(t *testing.T, body []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "out")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
