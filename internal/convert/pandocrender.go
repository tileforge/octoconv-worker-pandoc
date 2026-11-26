package convert

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	pandocRenderDefaultEdge = 1280
	pandocRenderMinEdge     = 256
	pandocRenderMaxEdge     = 2048
	pandocBlankBelow        = 250
	pandocSpeckFloor        = 240
	pandocSpeckFew          = 16
)

var pandocRenderReadOnly = []string{
	"/lib",
	"/usr/lib",
	"/usr/share/pandoc",
	"/usr/bin/python3",
	"/usr/bin/python3.11",
	"/etc/ld.so.cache",
	"/etc/localtime",
	"/etc/fonts",
	"/usr/share/fonts",
	"/usr/share/fontconfig",
}

var pandocRenderEngines = []string{"pandoc", "weasyprint", "pdftoppm"}

// PandocRenderConverter draws one printed page as a square PNG.
type PandocRenderConverter struct {
	PandocConverter
}

func init() {
	Default.Register(PandocRenderConverter{})
}

func (PandocRenderConverter) Operation() string { return OperationRender }

func (PandocRenderConverter) Pairs() []Pair {
	var pairs []Pair
	seen := map[string]bool{}
	for _, pair := range (PandocConverter{}).Pairs() {
		if seen[pair.From] {
			continue
		}
		seen[pair.From] = true
		pairs = append(pairs, Pair{From: pair.From, To: "png"})
	}
	return pairs
}

func (c PandocRenderConverter) host() PandocConverter {
	if c.run != nil || c.sandbox != nil {
		return c.PandocConverter
	}
	found, ok := Default.LookupOp(OperationConvert, "md", "html")
	if !ok {
		return c.PandocConverter
	}
	h, ok := found.(*PandocConverter)
	if ok && h != nil {
		return *h
	}
	return c.PandocConverter
}

func pandocRenderEnv(jobDir string) []string {
	env := append([]string{}, pandocEngineEnv(jobDir)...)
	return append(env, "XDG_CACHE_HOME="+jobDir, "PYTHONPYCACHEPREFIX="+jobDir)
}

func (c PandocRenderConverter) layoutExec(jobDir string) (engineExec, bool) {
	host := c.host()
	if !host.sandbox.confines() {
		return engineExec{}, false
	}
	ex := host.sandbox.forJobRW(jobDir, nil, pandocRenderReadOnly, pandocRenderEngines).withEnv(pandocRenderEnv(jobDir))
	return ex, true
}

func (c PandocRenderConverter) runEngine(ctx context.Context, jobDir, name string, args ...string) error {
	host := c.host()
	if host.run != nil {
		return host.run(ctx, name, args...)
	}
	if ex, ok := c.layoutExec(jobDir); ok {
		_, err := ex.run(ctx, name, args...)
		return err
	}
	return runCommandEnv(ctx, pandocRenderEnv(jobDir), name, args...)
}

func (c PandocRenderConverter) Convert(ctx context.Context, inPath, outPath string, opts map[string]any) error {
	if err := c.render(ctx, inPath, outPath, opts); err != nil {
		_ = os.Remove(outPath)
		return err
	}
	return nil
}

func (c PandocRenderConverter) render(ctx context.Context, inPath, outPath string, opts map[string]any) error {
	edge, err := pandocRenderEdge(opts)
	if err != nil {
		return err
	}
	src := NormalizeFormat(filepath.Ext(inPath))
	tgt := NormalizeFormat(filepath.Ext(outPath))
	if !pandocRenderPair(src, tgt) {
		return fmt.Errorf("unsupported conversion %s -> %s", src, tgt)
	}
	limit := c.host().MaxInputBytes
	if limit <= 0 {
		limit = DefaultMaxInputBytes
	}
	if err := enforcePandocInputSize(inPath, limit); err != nil {
		return err
	}

	jobDir := filepath.Dir(outPath)
	var htmlPath, pdfPath, prefix, mediaDir string
	defer func() {
		if htmlPath != "" {
			_ = os.Remove(htmlPath)
		}
		if pdfPath != "" {
			_ = os.Remove(pdfPath)
		}
		if mediaDir != "" {
			_ = os.RemoveAll(mediaDir)
		}
		if prefix != "" {
			matches, _ := filepath.Glob(prefix + "-*.png")
			for _, match := range matches {
				_ = os.Remove(match)
			}
			_ = os.Remove(prefix)
		}
	}()

	mediaDir, err = os.MkdirTemp(jobDir, ".pandoc-render-media-*")
	if err != nil {
		return err
	}
	if mediaDir == inPath || mediaDir == outPath {
		return fmt.Errorf("temp path")
	}
	htmlPath, err = pandocRenderTemp(jobDir, ".pandoc-render-*.html", inPath, outPath)
	if err != nil {
		return err
	}
	pdfPath, err = pandocRenderTemp(jobDir, ".pandoc-render-*.pdf", inPath, outPath)
	if err != nil {
		return err
	}
	prefix, err = pandocRenderTemp(jobDir, ".pandoc-render-*", inPath, outPath)
	if err != nil {
		return err
	}
	if err := os.Remove(prefix); err != nil {
		return err
	}

	pandocArgs := []string{
		"-f", pandocReaderSpec(src),
		"-t", "html",
		"--sandbox",
		"--standalone",
		"--extract-media=" + mediaDir,
		inPath,
		"-o", htmlPath,
	}
	if err := c.runEngine(ctx, jobDir, "pandoc", pandocArgs...); err != nil {
		return err
	}
	if err := pandocRenderImages(htmlPath, jobDir); err != nil {
		return err
	}
	weasyArgs := []string{"--quiet", "--encoding", "utf-8", "--media-type", "print", htmlPath, pdfPath}
	if err := c.runEngine(ctx, jobDir, "weasyprint", weasyArgs...); err != nil {
		return err
	}
	ppmArgs := []string{"-png", "-scale-to", strconv.Itoa(edge), pdfPath, prefix}
	if err := c.runEngine(ctx, jobDir, "pdftoppm", ppmArgs...); err != nil {
		return err
	}
	pages, err := pandocRenderPages(prefix)
	if err != nil {
		return err
	}
	for _, page := range pages {
		img, err := pandocDecodePNG(page)
		if err != nil {
			return err
		}
		if err := pandocPageEdge(img, edge); err != nil {
			return err
		}
		if pandocPageClean(img) {
			continue
		}
		boxed, err := pandocLetterbox(img, edge)
		if err != nil {
			return err
		}
		return writePNG(outPath, boxed)
	}
	return writeWhiteSquare(outPath, edge)
}

func pandocRenderPair(src, tgt string) bool {
	if tgt != "png" {
		return false
	}
	for _, pair := range (PandocRenderConverter{}).Pairs() {
		if pair.From == src {
			return true
		}
	}
	return false
}

func pandocRenderTemp(dir, pattern, inPath, outPath string) (string, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	name := f.Name()
	closeErr := f.Close()
	if name == inPath || name == outPath {
		_ = os.Remove(name)
		return "", fmt.Errorf("temp path")
	}
	if closeErr != nil {
		_ = os.Remove(name)
		return "", closeErr
	}
	return name, nil
}

func pandocRenderEdge(opts map[string]any) (int, error) {
	if len(opts) == 0 {
		return pandocRenderDefaultEdge, nil
	}
	if len(opts) != 1 {
		return 0, fmt.Errorf("size")
	}
	raw, ok := opts["size"]
	if !ok {
		return 0, fmt.Errorf("size")
	}
	n, ok := pandocRenderSize(raw)
	if !ok || n < pandocRenderMinEdge || n > pandocRenderMaxEdge {
		return 0, fmt.Errorf("size")
	}
	return n, nil
}

func pandocRenderSize(raw any) (int, bool) {
	switch v := raw.(type) {
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		if v > int64(math.MaxInt) || v < int64(math.MinInt) {
			return 0, false
		}
		return int(v), true
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) {
			return 0, false
		}
		if v > float64(math.MaxInt) || v < float64(math.MinInt) {
			return 0, false
		}
		return int(v), true
	default:
		return 0, false
	}
}

func pandocRenderImages(htmlPath, jobDir string) error {
	body, err := os.ReadFile(htmlPath)
	if err != nil {
		return fmt.Errorf("read html: %w", err)
	}
	jobRoot, err := filepath.EvalSymlinks(jobDir)
	if err != nil {
		return err
	}
	htmlDir := filepath.Dir(htmlPath)
	for _, src := range pandocImgSrcs(string(body)) {
		if pandocRemoteSrc(src) {
			continue
		}
		resolved, err := pandocResolveImage(src, htmlDir)
		if err != nil {
			return err
		}
		info, err := os.Stat(resolved)
		if err != nil || info.IsDir() {
			return fmt.Errorf("image %s", src)
		}
		eval, err := filepath.EvalSymlinks(resolved)
		if err != nil {
			return err
		}
		if !pandocInside(eval, jobRoot) {
			return fmt.Errorf("image %s", src)
		}
		if err := pandocDecodeImage(eval); err != nil {
			return fmt.Errorf("image %s", src)
		}
	}
	return nil
}

func pandocDecodeImage(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, 512)
	n, err := f.Read(head)
	if n == 0 {
		return fmt.Errorf("image")
	}
	if err != nil && err != io.EOF {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if pandocMarkup(head[:n]) {
		return pandocSVGRoot(f)
	}
	cfg, _, err := image.DecodeConfig(f)
	if err != nil || cfg.Width < 1 || cfg.Height < 1 {
		return fmt.Errorf("image")
	}
	return nil
}

func pandocMarkup(head []byte) bool {
	if bytes.HasPrefix(head, []byte{0xEF, 0xBB, 0xBF}) {
		head = head[3:]
	}
	head = bytes.TrimLeft(head, " \t\r\n")
	return bytes.HasPrefix(head, []byte("<"))
}

func pandocSVGRoot(r io.Reader) error {
	dec := xml.NewDecoder(r)
	for {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("image")
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if start.Name.Local != "svg" {
			return fmt.Errorf("image")
		}
		return nil
	}
}

func pandocRemoteSrc(src string) bool {
	s := strings.ToLower(strings.TrimSpace(src))
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func pandocResolveImage(src, htmlDir string) (string, error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return "", fmt.Errorf("image src")
	}
	if filepath.IsAbs(src) {
		return filepath.Clean(src), nil
	}
	return filepath.Clean(filepath.Join(htmlDir, src)), nil
}

func pandocInside(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func pandocASCIILower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c - 'A' + 'a'
		}
	}
	return string(b)
}

func pandocImgSrcs(html string) []string {
	lower := pandocASCIILower(html)
	var srcs []string
	for i := 0; i < len(lower); {
		j := strings.Index(lower[i:], "<img")
		if j < 0 {
			break
		}
		j += i
		if j+4 < len(lower) && pandocNameChar(lower[j+4]) {
			i = j + 4
			continue
		}
		end := strings.IndexByte(lower[j:], '>')
		if end < 0 {
			break
		}
		tag := html[j : j+end]
		if src, ok := pandocAttr(tag, "src"); ok {
			srcs = append(srcs, src)
		}
		i = j + end + 1
	}
	return srcs
}

func pandocNameChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '-' || b == ':'
}

func pandocAttr(tag, name string) (string, bool) {
	lower := pandocASCIILower(tag)
	key := pandocASCIILower(name)
	for i := 0; i < len(lower); {
		j := strings.Index(lower[i:], key)
		if j < 0 {
			return "", false
		}
		j += i
		if j > 0 && pandocNameChar(lower[j-1]) {
			i = j + len(key)
			continue
		}
		rest := j + len(key)
		if rest < len(lower) && pandocNameChar(lower[rest]) {
			i = rest
			continue
		}
		k := rest
		for k < len(tag) && pandocSpace(tag[k]) {
			k++
		}
		if k >= len(tag) || tag[k] != '=' {
			i = rest
			continue
		}
		k++
		for k < len(tag) && pandocSpace(tag[k]) {
			k++
		}
		if k >= len(tag) {
			return "", false
		}
		if tag[k] == '"' || tag[k] == '\'' {
			q := tag[k]
			k++
			end := strings.IndexByte(tag[k:], q)
			if end < 0 {
				return "", false
			}
			return tag[k : k+end], true
		}
		end := k
		for end < len(tag) && !pandocSpace(tag[end]) && tag[end] != '>' {
			end++
		}
		return tag[k:end], true
	}
	return "", false
}

func pandocSpace(b byte) bool {
	return b == ' ' || b == '\n' || b == '\r' || b == '\t'
}

func pandocPageNumber(mid string) (int, error) {
	if mid == "" {
		return 0, fmt.Errorf("page number")
	}
	for i := 0; i < len(mid); i++ {
		if mid[i] < '0' || mid[i] > '9' {
			return 0, fmt.Errorf("page number")
		}
	}
	n, err := strconv.Atoi(mid)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("page number")
	}
	return n, nil
}

func pandocRenderPages(prefix string) ([]string, error) {
	dir := filepath.Dir(prefix)
	base := filepath.Base(prefix)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	found := map[int]string{}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, base+"-") || !strings.HasSuffix(name, ".png") {
			continue
		}
		mid := strings.TrimSuffix(strings.TrimPrefix(name, base+"-"), ".png")
		n, convErr := pandocPageNumber(mid)
		if convErr != nil {
			return nil, fmt.Errorf("page name %s", name)
		}
		if _, ok := found[n]; ok {
			return nil, fmt.Errorf("duplicate page %d", n)
		}
		found[n] = filepath.Join(dir, name)
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("no pages")
	}
	paths := make([]string, len(found))
	for n := 1; n <= len(found); n++ {
		path, ok := found[n]
		if !ok {
			return nil, fmt.Errorf("missing page %d", n)
		}
		paths[n-1] = path
	}
	return paths, nil
}

func pandocDecodePNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	return img, nil
}

func pandocPageEdge(img image.Image, edge int) error {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	longer := w
	if h > longer {
		longer = h
	}
	if longer != edge || w > edge || h > edge {
		return fmt.Errorf("page bounds %d by %d", w, h)
	}
	return nil
}

func pandocFlatten(c color.Color) (uint8, uint8, uint8) {
	r, g, b, a := c.RGBA()
	return uint8((r + (0xffff - a)) >> 8), uint8((g + (0xffff - a)) >> 8), uint8((b + (0xffff - a)) >> 8)
}

func pandocPageClean(img image.Image) bool {
	b := img.Bounds()
	specks := map[[2]int]struct{}{}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl := pandocFlatten(img.At(x, y))
			if r < pandocSpeckFloor || g < pandocSpeckFloor || bl < pandocSpeckFloor {
				return false
			}
			if r < pandocBlankBelow || g < pandocBlankBelow || bl < pandocBlankBelow {
				specks[[2]int{x, y}] = struct{}{}
				if len(specks) > pandocSpeckFew {
					return false
				}
			}
		}
	}
	for pix := range specks {
		if _, ok := specks[[2]int{pix[0] + 1, pix[1]}]; ok {
			return false
		}
		if _, ok := specks[[2]int{pix[0], pix[1] + 1}]; ok {
			return false
		}
	}
	return true
}

func pandocLetterbox(src image.Image, edge int) (*image.NRGBA, error) {
	if err := pandocPageEdge(src, edge); err != nil {
		return nil, err
	}
	b := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, edge, edge))
	for i := range dst.Pix {
		dst.Pix[i] = 255
	}
	ox := (edge - b.Dx()) / 2
	oy := (edge - b.Dy()) / 2
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			dst.Set(ox+x, oy+y, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst, nil
}

func writeWhiteSquare(path string, edge int) error {
	img := image.NewNRGBA(image.Rect(0, 0, edge, edge))
	white := color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	for y := 0; y < edge; y++ {
		for x := 0; x < edge; x++ {
			img.SetNRGBA(x, y, white)
		}
	}
	return writePNG(path, img)
}

func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	err = png.Encode(f, img)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
