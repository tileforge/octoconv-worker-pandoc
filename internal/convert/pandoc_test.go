package convert

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPairsMatchManifest(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "schema", "manifest-pandoc-convert.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Capabilities []struct {
			Formats []Pair `json:"formats"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	got := (PandocConverter{}).Pairs()
	if len(doc.Capabilities) != 1 || len(got) != 32 || len(doc.Capabilities[0].Formats) != len(got) {
		t.Fatalf("pairs %d manifest %d", len(got), len(doc.Capabilities[0].Formats))
	}
	seen := map[Pair]bool{}
	for i, pair := range got {
		want := doc.Capabilities[0].Formats[i]
		if pair != want {
			t.Fatalf("pair %d %+v want %+v", i, pair, want)
		}
		if pair.From == pair.To {
			t.Fatalf("identity %s", pair.From)
		}
		seen[pair] = true
		if _, ok := Default.LookupOp(OperationConvert, pair.From, pair.To); !ok {
			t.Fatalf("lookup %s to %s", pair.From, pair.To)
		}
	}
	for _, absent := range []Pair{{From: "latex", To: "pdf"}, {From: "epub", To: "latex"}, {From: "epub", To: "org"}} {
		if seen[absent] {
			t.Fatalf("listed %s to %s", absent.From, absent.To)
		}
	}
}

func TestLimitsAndMIME(t *testing.T) {
	if MaxFileBytes != 100<<20 || DefaultMaxInputBytes != 3145728 || DefaultEngineTimeout.Seconds() != 65 {
		t.Fatalf("limits file %d input %d timeout %s", MaxFileBytes, DefaultMaxInputBytes, DefaultEngineTimeout)
	}
	want := map[string]string{
		"html":  "text/html",
		"md":    "text/markdown",
		"rst":   "text/x-rst",
		"org":   "text/plain",
		"latex": "application/x-latex",
		"docx":  "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"epub":  "application/epub+zip",
	}
	for format, mime := range want {
		if got := MIMEType(format); got != mime {
			t.Errorf("%s %s", format, got)
		}
	}
}

func TestConvertArgs(t *testing.T) {
	cases := []struct {
		src, tgt string
		opts     pandocOpts
		want     string
	}{
		{"md", "html", pandocOpts{}, "-f markdown-raw_html-raw_tex -t html --sandbox --standalone /in.md -o /out.html"},
		{"rst", "html", pandocOpts{}, "-f rst -t html --sandbox --standalone /in.rst -o /out.html"},
		{"org", "md", pandocOpts{}, "-f org -t markdown --sandbox /in.org -o /out.md"},
		{"latex", "md", pandocOpts{}, "-f latex-raw_tex -t markdown --sandbox /in.latex -o /out.md"},
		{"html", "rst", pandocOpts{}, "-f html-raw_html-raw_tex -t rst --sandbox /in.html -o /out.rst"},
		{"docx", "epub", pandocOpts{}, "-f docx -t epub --sandbox --standalone /in.docx -o /out.epub"},
		{"epub", "docx", pandocOpts{}, "-f epub-raw_html-raw_tex -t docx --sandbox --standalone /in.epub -o /out.docx"},
		{"md", "html", pandocOpts{Standalone: true}, "-f markdown-raw_html-raw_tex -t html --sandbox --standalone /in.md -o /out.html"},
		{"html", "md", pandocOpts{Standalone: true}, "-f html-raw_html-raw_tex -t markdown --sandbox --standalone /in.html -o /out.md"},
	}
	for _, tc := range cases {
		args := pandocConvertArgs(tc.src, tc.tgt, "/in."+tc.src, "/out."+tc.tgt, tc.opts)
		if strings.Join(args, " ") != tc.want {
			t.Fatalf("%s to %s %+v\n got %s\nwant %s", tc.src, tc.tgt, tc.opts, strings.Join(args, " "), tc.want)
		}
		for _, arg := range args {
			if strings.Contains(arg, "+raw") || strings.Contains(strings.ToLower(arg), "network") {
				t.Fatalf("forbidden arg %q in %s", arg, args)
			}
		}
	}
	latex := strings.Join(pandocConvertArgs("latex", "html", "/in.latex", "/out.html", pandocOpts{}), " ")
	if strings.Contains(latex, "raw_html") || !strings.Contains(latex, "--standalone") {
		t.Fatal(latex)
	}
}

func TestConvertRejectsOptionsAndUnknownPairsBeforeRun(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.md")
	out := filepath.Join(dir, "out.html")
	if err := os.WriteFile(in, []byte("# hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got []string
	called := false
	c := &PandocConverter{MaxInputBytes: 1 << 20, run: func(_ context.Context, _ string, args ...string) error {
		called = true
		got = append([]string(nil), args...)
		return os.WriteFile(out, []byte("<!doctype html><html></html>"), 0o600)
	}}
	for _, opts := range []map[string]any{{"wrap": "sideways"}, {"language": "eng"}} {
		called = false
		err := c.Convert(context.Background(), in, out, opts)
		if err == nil || called {
			t.Fatalf("accepted %v err %v called %v", opts, err, called)
		}
	}
	accepted := []struct {
		opts map[string]any
		flag string
	}{
		{map[string]any{"standalone": true}, "--standalone"},
		{map[string]any{"toc": true}, "--toc"},
		{map[string]any{"number_sections": true}, "--number-sections"},
		{map[string]any{"section_divs": true}, "--section-divs"},
		{map[string]any{"wrap": "auto"}, "--wrap=auto"},
		{map[string]any{"wrap": "none"}, "--wrap=none"},
		{map[string]any{"wrap": "preserve"}, "--wrap=preserve"},
	}
	for _, tc := range accepted {
		called = false
		err := c.Convert(context.Background(), in, out, tc.opts)
		if err != nil || !called || !containsArg(got, tc.flag) {
			t.Fatalf("opts %v err %v called %v args %v", tc.opts, err, called, got)
		}
	}
	for _, opts := range []map[string]any{nil, {}, {"standalone": false}, {"wrap": ""}, {"toc": false, "section_divs": false}} {
		called = false
		err := c.Convert(context.Background(), in, out, opts)
		if err != nil || !called {
			t.Fatalf("opts %v err %v called %v", opts, err, called)
		}
	}
	called = false
	if err := c.Convert(context.Background(), in, filepath.Join(dir, "out.md"), nil); err == nil || called {
		t.Fatalf("identity err %v called %v", err, called)
	}
	latex := filepath.Join(dir, "in.latex")
	if err := os.WriteFile(latex, []byte(`\documentclass{article}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.Convert(context.Background(), latex, filepath.Join(dir, "out.pdf"), nil); err == nil || called {
		t.Fatalf("latex pdf err %v called %v", err, called)
	}
	epub := filepath.Join(dir, "in.epub")
	if err := os.WriteFile(epub, []byte("PK"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.Convert(context.Background(), epub, filepath.Join(dir, "out.latex"), nil); err == nil || called {
		t.Fatalf("epub latex err %v called %v", err, called)
	}
}

func TestConvertCapturesStructuralArgsAndAcceptsEmptyOrHTML(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.md")
	if err := os.WriteFile(in, []byte("# hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got []string
	c := &PandocConverter{run: func(_ context.Context, name string, args ...string) error {
		if name != "pandoc" {
			return errors.New(name)
		}
		got = append([]string(nil), args...)
		return os.WriteFile(filepath.Join(dir, "out.html"), nil, 0o600)
	}}
	out := filepath.Join(dir, "out.html")
	if err := c.Convert(context.Background(), in, out, nil); err != nil {
		t.Fatal(err)
	}
	want := "-f markdown-raw_html-raw_tex -t html --sandbox --standalone " + in + " -o " + out
	if strings.Join(got, " ") != want {
		t.Fatalf("args %s", strings.Join(got, " "))
	}
	if c.MaxInputBytes != 0 {
		t.Fatal(c.MaxInputBytes)
	}

	c.run = func(_ context.Context, name string, args ...string) error {
		got = append([]string(nil), args...)
		return os.WriteFile(filepath.Join(dir, "out.md"), []byte("ok\n"), 0o600)
	}
	mdOut := filepath.Join(dir, "out.md")
	htmlIn := filepath.Join(dir, "in.html")
	if err := os.WriteFile(htmlIn, []byte("<!doctype html><html></html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.Convert(context.Background(), htmlIn, mdOut, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(got, " "), "--standalone") {
		t.Fatalf("standalone on markdown %s", got)
	}

	c.run = func(context.Context, string, ...string) error {
		return os.WriteFile(out, []byte("<!doctype html><html></html>"), 0o600)
	}
	if err := c.Convert(context.Background(), in, out, nil); err != nil {
		t.Fatal(err)
	}
	c.run = func(context.Context, string, ...string) error {
		return os.WriteFile(out, []byte("not html"), 0o600)
	}
	if err := c.Convert(context.Background(), in, out, nil); err == nil {
		t.Fatal("accepted plain text as html")
	}
	latexOut := filepath.Join(dir, "out.latex")
	c.run = func(context.Context, string, ...string) error {
		body := "% Options for packages loaded elsewhere\n\\PassOptionsToPackage{unicode}{hyperref}\n"
		return os.WriteFile(latexOut, []byte(body), 0o600)
	}
	if err := c.Convert(context.Background(), in, latexOut, nil); err != nil {
		t.Fatal(err)
	}
	c.run = func(context.Context, string, ...string) error {
		return os.WriteFile(latexOut, []byte("% only a comment\n"), 0o600)
	}
	if err := c.Convert(context.Background(), in, latexOut, nil); err == nil {
		t.Fatal("accepted a comment-only latex file")
	}
}

func TestInputCeilingRejectsTheFileAndTheDeclaredZip(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.md")
	out := filepath.Join(dir, "out.html")
	if err := os.WriteFile(in, bytes.Repeat([]byte("a"), 64), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	c := &PandocConverter{MaxInputBytes: 32, run: func(context.Context, string, ...string) error {
		called = true
		return nil
	}}
	err := c.Convert(context.Background(), in, out, nil)
	if !errors.Is(err, ErrPandocInputTooLarge) || called {
		t.Fatalf("err %v called %v", err, called)
	}

	bomb := filepath.Join(dir, "bomb.docx")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(bytes.Repeat([]byte("A"), 8<<20)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bomb, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(bomb)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 1<<20 {
		t.Fatalf("fixture %d bytes", info.Size())
	}
	called = false
	c.MaxInputBytes = 1 << 20
	err = c.Convert(context.Background(), bomb, filepath.Join(dir, "out.md"), nil)
	if !errors.Is(err, ErrPandocInputTooLarge) || called {
		t.Fatalf("bomb err %v called %v", err, called)
	}

	plain := filepath.Join(dir, "plain.md")
	if err := os.WriteFile(plain, bytes.Repeat([]byte("a"), 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := enforcePandocInputSize(plain, 1<<20); err != nil {
		t.Fatal(err)
	}
	if err := enforcePandocInputSize(plain, 1<<62); err != nil {
		t.Fatal(err)
	}

	wrap := filepath.Join(dir, "wrap.docx")
	buf.Reset()
	zw = zip.NewWriter(&buf)
	for _, name := range []string{"word/document.xml", "word/second.xml"} {
		header := &zip.FileHeader{Name: name, Method: zip.Store}
		writer, err := zw.CreateRaw(header)
		if err != nil {
			t.Fatal(err)
		}
		header.UncompressedSize64 = 1 << 63
		header.CompressedSize64 = 0
		if _, err := writer.Write(nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wrap, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := enforcePandocInputSize(wrap, 1<<20); !errors.Is(err, ErrPandocInputTooLarge) {
		t.Fatal(err)
	}

	notZip := filepath.Join(dir, "note.docx")
	if err := os.WriteFile(notZip, []byte("not a zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	noteOut := filepath.Join(dir, "note.md")
	called = false
	c.run = func(context.Context, string, ...string) error {
		called = true
		return os.WriteFile(noteOut, []byte("ok\n"), 0o600)
	}
	err = c.Convert(context.Background(), notZip, noteOut, nil)
	if !called || err != nil {
		t.Fatalf("not zip err %v called %v", err, called)
	}
}

func TestContainerOutput(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.md")
	if err := os.WriteFile(in, []byte("# hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	docx := filepath.Join(dir, "out.docx")
	epub := filepath.Join(dir, "out.epub")
	c := &PandocConverter{run: func(context.Context, string, ...string) error {
		return writeZip(docx, map[string]string{"word/document.xml": "<document/>"})
	}}
	if err := c.Convert(context.Background(), in, docx, nil); err != nil {
		t.Fatal(err)
	}
	c.run = func(context.Context, string, ...string) error {
		return writeZip(epub, map[string]string{"mimetype": "application/epub+zip"})
	}
	if err := c.Convert(context.Background(), in, epub, nil); err != nil {
		t.Fatal(err)
	}
	c.run = func(context.Context, string, ...string) error {
		return writeZip(docx, map[string]string{"mimetype": "application/epub+zip"})
	}
	if err := c.Convert(context.Background(), in, docx, nil); err == nil {
		t.Fatal("accepted an epub container as docx")
	}
}

func writeZip(path string, files map[string]string) error {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		if _, err := w.Write([]byte(body)); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o600)
}

func TestGoSourcesDoNotNameLandlock(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(bytes.ToLower(body), []byte("landlock")) {
			t.Fatalf("%s names landlock", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
