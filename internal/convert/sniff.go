package convert

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"math"
	"unicode/utf8"
)

const sniffCap = 1 << 20

func readSniffPrefix(r io.ReaderAt, size int64) ([]byte, bool) {
	if size < 0 {
		return nil, false
	}
	n := size
	if n > sniffCap {
		n = sniffCap
	}
	buf := make([]byte, n)
	if _, err := r.ReadAt(buf, 0); err != nil && err != io.EOF {
		return nil, false
	}
	if bytes.Contains(buf, []byte{0}) || !utf8.Valid(buf) {
		return nil, false
	}
	return buf, true
}

func looksLikeHTML(prefix []byte) bool {
	buf := bytes.TrimPrefix(prefix, []byte{0xEF, 0xBB, 0xBF})
	buf = bytes.TrimLeft(buf, " \t\r\n\f\v")
	lower := bytes.ToLower(buf)
	if bytes.HasPrefix(lower, []byte("<!doctype html")) {
		return true
	}
	const tag = "<html"
	if !bytes.HasPrefix(lower, []byte(tag)) || len(lower) == len(tag) {
		return false
	}
	switch lower[len(tag)] {
	case '>', '/', ' ', '\t', '\n', '\r', '\f', '\v':
		return true
	default:
		return false
	}
}

func looksLikeLaTeX(prefix []byte) bool {
	buf := bytes.TrimPrefix(prefix, []byte{0xEF, 0xBB, 0xBF})
	buf = skipLaTeXComments(buf)
	if len(buf) == 0 {
		return false
	}
	if bytes.HasPrefix(buf, []byte(`\documentclass`)) || bytes.HasPrefix(buf, []byte(`\begin{`)) {
		return true
	}
	return buf[0] == '\\' && len(buf) > 1 && isASCIILetter(buf[1])
}

func skipLaTeXComments(buf []byte) []byte {
	for {
		buf = bytes.TrimLeft(buf, " \t\r\n\f\v")
		if len(buf) == 0 || buf[0] != '%' {
			return buf
		}
		nl := bytes.IndexByte(buf, '\n')
		if nl < 0 {
			return nil
		}
		buf = buf[nl+1:]
	}
}

func isASCIILetter(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

func looksLikeText(prefix []byte) bool {
	for _, b := range prefix {
		if b >= 0x20 {
			continue
		}
		switch b {
		case '\t', '\n', '\r', '\f':
		default:
			return false
		}
	}
	return true
}

func validatePandocOutput(tgt, outPath string) error {
	f, info, err := OpenJobFile(outPath)
	if err != nil {
		return fmt.Errorf("stat output: %w", err)
	}
	defer f.Close()
	if info.Size() == 0 {
		return nil
	}
	switch tgt {
	case "html", "latex", "md", "rst", "org":
		prefix, ok := readSniffPrefix(f, info.Size())
		if !ok {
			return fmt.Errorf("output failed %s content validation", tgt)
		}
		ok = false
		switch tgt {
		case "html":
			ok = looksLikeHTML(prefix)
		case "latex":
			ok = looksLikeLaTeX(prefix)
		default:
			ok = looksLikeText(prefix)
		}
		if !ok {
			return fmt.Errorf("output failed %s content validation", tgt)
		}
		return nil
	case "docx", "epub":
		kind, err := containerKind(f, info.Size())
		if err != nil {
			return fmt.Errorf("output failed container validation: %w", err)
		}
		if kind != tgt {
			return fmt.Errorf("output container format %q, want %q", kind, tgt)
		}
		return nil
	default:
		return fmt.Errorf("no output validator for target %q", tgt)
	}
}

func declaredUncompressed(r io.ReaderAt, size int64) (uint64, bool, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return 0, false, nil
	}
	var total uint64
	for _, file := range zr.File {
		next := total + file.UncompressedSize64
		if next < total {
			return math.MaxUint64, true, nil
		}
		total = next
	}
	return total, true, nil
}

func containerKind(r io.ReaderAt, size int64) (string, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return "", err
	}
	var kind string
	for _, file := range zr.File {
		switch file.Name {
		case "word/document.xml":
			if kind == "" {
				kind = "docx"
			}
		case "mimetype":
			rc, err := file.Open()
			if err != nil {
				return "", err
			}
			body, err := io.ReadAll(io.LimitReader(rc, 64))
			rc.Close()
			if err != nil {
				return "", err
			}
			if string(body) == "application/epub+zip" {
				kind = "epub"
			}
		}
	}
	if kind == "" {
		return "", fmt.Errorf("unrecognized container")
	}
	return kind, nil
}
