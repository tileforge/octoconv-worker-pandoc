package convert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type scriptedCall struct {
	name string
	args []string
}

func newScripted(on func(ctx context.Context, name string, args []string) error) (PandocRenderConverter, *[]scriptedCall) {
	var calls []scriptedCall
	c := PandocRenderConverter{PandocConverter: PandocConverter{run: func(ctx context.Context, name string, args ...string) error {
		calls = append(calls, scriptedCall{name, append([]string(nil), args...)})
		if on != nil {
			return on(ctx, name, args)
		}
		return nil
	}}}
	return c, &calls
}

func scriptTouch(name string, args []string) error {
	switch name {
	case "pandoc":
		return os.WriteFile(argAfter(args, "-o"), []byte("<html></html>\n"), 0o600)
	case "weasyprint":
		return os.WriteFile(args[len(args)-1], []byte("%PDF-1.4\n"), 0o600)
	default:
		return nil
	}
}

func argAfter(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func scaleArg(args []string) string {
	return argAfter(args, "-scale-to")
}

func countExtract(args []string) (int, string) {
	n := 0
	dir := ""
	for _, arg := range args {
		if strings.HasPrefix(arg, "--extract-media=") {
			n++
			dir = strings.TrimPrefix(arg, "--extract-media=")
		}
	}
	return n, dir
}

func forbidRenderFlags(t *testing.T, args []string) {
	t.Helper()
	for _, arg := range args {
		switch arg {
		case "--pdf-engine", "--toc", "--number-sections", "--section-divs", "--wrap":
			t.Fatalf("forbidden %s in %v", arg, args)
		}
		if strings.HasPrefix(arg, "--pdf-engine=") || strings.HasPrefix(arg, "--wrap=") || strings.HasPrefix(arg, "--toc=") {
			t.Fatalf("forbidden %s", arg)
		}
	}
}

func writeSolid(path string, w, h int, fill color.NRGBA, paint func(*image.NRGBA)) error {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	if fill.A != 0 || fill.R != 0 || fill.G != 0 || fill.B != 0 {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				img.SetNRGBA(x, y, fill)
			}
		}
	}
	if paint != nil {
		paint(img)
	}
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

func contentPage(prefix string, edge int) error {
	return writeSolid(prefix+"-1.png", edge, edge/2, color.NRGBA{255, 255, 255, 255}, func(img *image.NRGBA) {
		img.SetNRGBA(edge/2, edge/4, color.NRGBA{0, 0, 0, 255})
	})
}

func decodePNG(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func isWhite(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return uint8(r>>8) == 255 && uint8(g>>8) == 255 && uint8(b>>8) == 255
}

func assertWhiteSquare(t *testing.T, path string, edge int) {
	t.Helper()
	img := decodePNG(t, path)
	if img.Bounds().Dx() != edge || img.Bounds().Dy() != edge {
		t.Fatalf("bounds %v", img.Bounds())
	}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			if r != 0xffff || g != 0xffff || bl != 0xffff || a != 0xffff {
				t.Fatalf("pixel %d,%d", x, y)
			}
		}
	}
}

func assertNotAllWhite(t *testing.T, path string) {
	t.Helper()
	img := decodePNG(t, path)
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if !isWhite(img.At(x, y)) {
				return
			}
		}
	}
	t.Fatal("all white")
}

func absentOutput(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("output stat %v", err)
	}
}

func precreate(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("precreated"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPandocRenderManifest(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "schema", "manifest-pandoc-render.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Capabilities []struct {
			ID      string `json:"id"`
			Version int    `json:"version"`
			Legacy  struct {
				Operation string `json:"operation"`
				Engine    string `json:"engine"`
			} `json:"legacy"`
			ClientOperation string `json:"client_operation"`
			Inputs          []struct {
				Role string `json:"role"`
			} `json:"inputs"`
			Outputs []struct {
				Role string `json:"role"`
			} `json:"outputs"`
			ParameterSchema json.RawMessage `json:"parameter_schema"`
			Formats         []Pair          `json:"formats"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Capabilities) != 1 {
		t.Fatalf("capabilities %d", len(doc.Capabilities))
	}
	cap := doc.Capabilities[0]
	if cap.ID != "octoconv.render.pandoc" || cap.Version != 1 {
		t.Fatalf("id %s version %d", cap.ID, cap.Version)
	}
	if cap.Legacy.Operation != OperationRender || cap.Legacy.Engine != EnginePandoc || cap.ClientOperation != OperationRender {
		t.Fatalf("legacy %+v client %s", cap.Legacy, cap.ClientOperation)
	}
	if len(cap.Inputs) != 1 || cap.Inputs[0].Role != "source" || len(cap.Outputs) != 1 || cap.Outputs[0].Role != "primary" {
		t.Fatalf("inputs %+v outputs %+v", cap.Inputs, cap.Outputs)
	}
	var schema map[string]any
	if err := json.Unmarshal(cap.ParameterSchema, &schema); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(schema)
	if err != nil || string(encoded) != "{}" || len(schema) != 0 {
		t.Fatalf("parameter_schema %s", cap.ParameterSchema)
	}
	wantFrom := []string{"md", "rst", "org", "latex", "html", "docx", "epub"}
	if len(cap.Formats) != len(wantFrom) {
		t.Fatalf("formats %d", len(cap.Formats))
	}
	pairs := (PandocRenderConverter{}).Pairs()
	if len(pairs) != len(wantFrom) {
		t.Fatalf("pairs %d", len(pairs))
	}
	for i, from := range wantFrom {
		if cap.Formats[i] != (Pair{From: from, To: "png"}) || pairs[i] != cap.Formats[i] {
			t.Fatalf("%d format %+v pair %+v", i, cap.Formats[i], pairs[i])
		}
		got, ok := Default.LookupOp(OperationRender, from, "png")
		if !ok {
			t.Fatalf("missing %s", from)
		}
		if _, is := got.(PandocRenderConverter); !is {
			t.Fatalf("%s type %T", from, got)
		}
	}
	conv, ok := Default.LookupOp(OperationConvert, "md", "html")
	if !ok {
		t.Fatal("convert pair missing")
	}
	if _, is := conv.(*PandocConverter); !is {
		t.Fatalf("convert type %T", conv)
	}
	if MIMEType("png") != "image/png" {
		t.Fatal(MIMEType("png"))
	}
	reg := NewRegistry()
	reg.Register(&PandocConverter{})
	reg.Register(PandocRenderConverter{})

	docker, err := os.ReadFile(filepath.Join("..", "..", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(docker)
	for _, name := range []string{"ca-certificates", "pandoc", "weasyprint", "poppler-utils", "fonts-dejavu-core", "--no-install-recommends"} {
		if !strings.Contains(text, name) {
			t.Fatalf("dockerfile missing %s", name)
		}
	}
	for _, name := range []string{"tectonic", "texlive", "wkhtmltopdf", "chromium", "groff"} {
		if strings.Contains(text, name) {
			t.Fatalf("dockerfile names %s", name)
		}
	}
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	page := string(readme)
	for _, name := range []string{
		"octoconv.convert.pandoc",
		"3145728",
		"schema/manifest-pandoc-render.json",
		"octoconv.render.pandoc",
		"md", "rst", "org", "latex", "html", "docx", "epub",
		"png",
		"parameter_schema",
		"not approved",
		"1280",
		"256",
		"2048",
		"weasyprint",
		"pdftoppm",
	} {
		if !strings.Contains(page, name) {
			t.Fatalf("readme missing %s", name)
		}
	}
}

func TestPandocRenderFirstContent(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.md")
	out := filepath.Join(dir, "out.png")
	if err := os.WriteFile(in, []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, calls := newScripted(func(_ context.Context, name string, args []string) error {
		if err := scriptTouch(name, args); err != nil {
			return err
		}
		if name != "pdftoppm" {
			return nil
		}
		prefix := args[len(args)-1]
		white := color.NRGBA{255, 255, 255, 255}
		if err := writeSolid(prefix+"-1.png", 256, 256, white, nil); err != nil {
			return err
		}
		if err := writeSolid(prefix+"-2.png", 256, 128, white, nil); err != nil {
			return err
		}
		return writeSolid(prefix+"-3.png", 256, 128, white, func(img *image.NRGBA) {
			img.SetNRGBA(128, 64, color.NRGBA{0, 0, 0, 255})
		})
	})
	if err := c.Convert(context.Background(), in, out, map[string]any{"size": 256}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 3 || (*calls)[0].name != "pandoc" || (*calls)[1].name != "weasyprint" || (*calls)[2].name != "pdftoppm" {
		t.Fatalf("calls %+v", *calls)
	}
	pandocArgs := (*calls)[0].args
	if strings.Join(pandocArgs[:6], " ") != "-f markdown-raw_html-raw_tex -t html --sandbox --standalone" {
		t.Fatalf("pandoc %v", pandocArgs)
	}
	n, media := countExtract(pandocArgs)
	if n != 1 || !filepath.IsAbs(media) || !pandocInside(media, dir) {
		t.Fatalf("extract %d %s", n, media)
	}
	forbidRenderFlags(t, pandocArgs)
	weasy := (*calls)[1].args
	if len(weasy) != 7 || strings.Join(weasy[:5], " ") != "--quiet --encoding utf-8 --media-type print" {
		t.Fatalf("weasyprint %v", weasy)
	}
	ppm := (*calls)[2].args
	if len(ppm) != 5 || ppm[0] != "-png" || ppm[1] != "-scale-to" || ppm[2] != "256" {
		t.Fatalf("pdftoppm %v", ppm)
	}
	for _, bad := range []string{"-f", "-l", "-singlefile", "-r", "-cropbox"} {
		for _, arg := range ppm {
			if arg == bad {
				t.Fatalf("pdftoppm has %s", bad)
			}
		}
	}
	img := decodePNG(t, out)
	if img.Bounds().Dx() != 256 || img.Bounds().Dy() != 256 {
		t.Fatalf("bounds %v", img.Bounds())
	}
	if isWhite(img.At(128, 128)) {
		t.Fatal("center is white")
	}
	if !isWhite(img.At(0, 0)) {
		t.Fatal("corner is not white")
	}
}

func TestPandocRenderPageBreakIsNotASheet(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.md")
	out := filepath.Join(dir, "out.png")
	body := "alpha\n\n\\newpage\n\nbeta\n\n\\newpage\n\ngamma\n"
	if err := os.WriteFile(in, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, calls := newScripted(func(_ context.Context, name string, args []string) error {
		if err := scriptTouch(name, args); err != nil {
			return err
		}
		if name == "pdftoppm" {
			return contentPage(args[len(args)-1], 256)
		}
		return nil
	})
	if err := c.Convert(context.Background(), in, out, map[string]any{"size": 256}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 3 || (*calls)[0].name != "pandoc" || (*calls)[1].name != "weasyprint" || (*calls)[2].name != "pdftoppm" {
		t.Fatalf("calls %+v", *calls)
	}
	assertNotAllWhite(t, out)
}

func TestPandocRenderLetterbox(t *testing.T) {
	for _, tc := range []struct {
		w, h, x, y, bw, bh int
	}{
		{400, 200, 0, 64, 256, 128},
		{20, 40, 64, 0, 128, 256},
	} {
		dir := t.TempDir()
		in := filepath.Join(dir, "in.md")
		out := filepath.Join(dir, "out.png")
		pdfPath := filepath.Join(dir, "source.pdf")
		if err := os.WriteFile(in, []byte("page\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeFullBleedPDF(pdfPath, tc.w, tc.h); err != nil {
			t.Fatal(err)
		}
		c, _ := newScripted(func(ctx context.Context, name string, args []string) error {
			switch name {
			case "pandoc":
				return os.WriteFile(argAfter(args, "-o"), []byte("<html></html>\n"), 0o600)
			case "weasyprint":
				return copyFile(args[len(args)-1], pdfPath)
			case "pdftoppm":
				if err := rejectFit(dir); err != nil {
					return err
				}
				cmd := exec.CommandContext(ctx, "pdftoppm", args...)
				outBytes, err := cmd.CombinedOutput()
				if err != nil {
					return fmt.Errorf("%w %s", err, outBytes)
				}
				return nil
			default:
				return fmt.Errorf("unexpected %s", name)
			}
		})
		if err := c.Convert(context.Background(), in, out, map[string]any{"size": 256}); err != nil {
			t.Fatal(err)
		}
		if err := rejectFit(dir); err != nil {
			t.Fatal(err)
		}
		img := decodePNG(t, out)
		if img.Bounds().Dx() != 256 || img.Bounds().Dy() != 256 {
			t.Fatalf("%d by %d bounds %v", tc.w, tc.h, img.Bounds())
		}
		if !isWhite(img.At(0, 0)) || !isWhite(img.At(255, 0)) || !isWhite(img.At(0, 255)) || !isWhite(img.At(255, 255)) {
			t.Fatalf("%d by %d corner", tc.w, tc.h)
		}
		if isWhite(img.At(128, 128)) {
			t.Fatalf("%d by %d center", tc.w, tc.h)
		}
		minX, minY, maxX, maxY := 256, 256, -1, -1
		for y := 0; y < 256; y++ {
			for x := 0; x < 256; x++ {
				if isWhite(img.At(x, y)) {
					continue
				}
				if x < minX {
					minX = x
				}
				if y < minY {
					minY = y
				}
				if x > maxX {
					maxX = x
				}
				if y > maxY {
					maxY = y
				}
			}
		}
		if minX != tc.x || minY != tc.y || maxX-minX+1 != tc.bw || maxY-minY+1 != tc.bh {
			t.Fatalf("%d by %d box %d,%d %dx%d", tc.w, tc.h, minX, minY, maxX-minX+1, maxY-minY+1)
		}
	}
}

func rejectFit(dir string) error {
	return filepath.WalkDir(dir, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if filepath.Base(path) == "fit.png" {
			return fmt.Errorf("fit.png")
		}
		return nil
	})
}

func copyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := out.ReadFrom(in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func writeFullBleedPDF(path string, w, h int) error {
	var body bytes.Buffer
	body.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	writeObj := func(n int, s string) {
		offsets = append(offsets, body.Len())
		fmt.Fprintf(&body, "%d 0 obj\n%s\nendobj\n", n, s)
	}
	writeObj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObj(2, "<< /Type /Pages /Count 1 /Kids [3 0 R] >>")
	writeObj(3, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] /Contents 4 0 R /Resources << >> >>", w, h))
	stream := fmt.Sprintf("0 0 0 rg\n0 0 %d %d re\nf\n", w, h)
	writeObj(4, fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream))
	xref := body.Len()
	fmt.Fprintf(&body, "xref\n0 %d\n", len(offsets))
	fmt.Fprintf(&body, "0000000000 65535 f \n")
	for i := 1; i < len(offsets); i++ {
		fmt.Fprintf(&body, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&body, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return os.WriteFile(path, body.Bytes(), 0o600)
}

func TestPandocRenderUnserved(t *testing.T) {
	if _, ok := Default.LookupOp(OperationRender, "txt", "png"); ok {
		t.Fatal("txt")
	}
	if _, ok := Default.LookupOp(OperationRender, "pdf", "png"); ok {
		t.Fatal("pdf")
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "in.txt")
	out := filepath.Join(dir, "out.png")
	if err := os.WriteFile(in, []byte("text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	precreate(t, out)
	n := 0
	c := PandocRenderConverter{PandocConverter: PandocConverter{run: func(context.Context, string, ...string) error {
		n++
		return errors.New("ran")
	}}}
	if err := c.Convert(context.Background(), in, out, nil); err == nil {
		t.Fatal("nil error")
	}
	if n != 0 {
		t.Fatalf("runner %d", n)
	}
	absentOutput(t, out)
}

func TestPandocRenderCleanSheet(t *testing.T) {
	white := color.NRGBA{255, 255, 255, 255}
	speck := color.NRGBA{240, 240, 240, 255}
	cases := []struct {
		name   string
		clean  bool
		fill   color.NRGBA
		paint  func(*image.NRGBA)
		noFill bool
	}{
		{name: "255", clean: true, fill: white},
		{name: "250", clean: true, fill: color.NRGBA{250, 250, 250, 255}},
		{name: "one-249", clean: true, fill: white, paint: func(img *image.NRGBA) {
			img.SetNRGBA(3, 3, color.NRGBA{249, 250, 250, 255})
		}},
		{name: "sixteen", clean: true, fill: white, paint: func(img *image.NRGBA) {
			putSpecks(img, 16, speck)
		}},
		{name: "seventeen", clean: false, fill: white, paint: func(img *image.NRGBA) {
			putSpecks(img, 17, speck)
		}},
		{name: "touching", clean: false, fill: white, paint: func(img *image.NRGBA) {
			img.SetNRGBA(4, 4, speck)
			img.SetNRGBA(5, 4, speck)
		}},
		{name: "ink", clean: false, fill: white, paint: func(img *image.NRGBA) {
			img.SetNRGBA(3, 3, color.NRGBA{239, 239, 239, 255})
		}},
		{name: "cluster", clean: false, fill: white, paint: func(img *image.NRGBA) {
			for i := 0; i < 8; i++ {
				img.SetNRGBA(10+i, 6, color.NRGBA{0, 0, 0, 255})
			}
		}},
		{name: "transparent", clean: true, noFill: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			in := filepath.Join(dir, "in.md")
			out := filepath.Join(dir, "out.png")
			if err := os.WriteFile(in, []byte("x\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			var page []byte
			c, _ := newScripted(func(_ context.Context, name string, args []string) error {
				if err := scriptTouch(name, args); err != nil {
					return err
				}
				if name != "pdftoppm" {
					return nil
				}
				path := args[len(args)-1] + "-1.png"
				fill := tc.fill
				if tc.noFill {
					fill = color.NRGBA{}
				}
				if err := writeSolid(path, 256, 128, fill, tc.paint); err != nil {
					return err
				}
				var err error
				page, err = os.ReadFile(path)
				return err
			})
			if err := c.Convert(context.Background(), in, out, map[string]any{"size": 256}); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			if tc.clean {
				assertWhiteSquare(t, out, 256)
				if bytes.Equal(got, page) {
					t.Fatal("output is the raster")
				}
				return
			}
			assertNotAllWhite(t, out)
		})
	}
}

func putSpecks(img *image.NRGBA, n int, c color.NRGBA) {
	for i := 0; i < n; i++ {
		img.SetNRGBA(2+i*2, 2, c)
	}
}

func TestPandocRenderAllClean(t *testing.T) {
	for _, edge := range []int{256, 400} {
		dir := t.TempDir()
		in := filepath.Join(dir, "in.md")
		out := filepath.Join(dir, "out.png")
		if err := os.WriteFile(in, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var page []byte
		c, calls := newScripted(func(_ context.Context, name string, args []string) error {
			if err := scriptTouch(name, args); err != nil {
				return err
			}
			if name != "pdftoppm" {
				return nil
			}
			prefix := args[len(args)-1]
			for n := 1; n <= 2; n++ {
				path := fmt.Sprintf("%s-%d.png", prefix, n)
				if err := writeSolid(path, edge, edge, color.NRGBA{255, 255, 255, 255}, func(img *image.NRGBA) {
					putSpecks(img, 16, color.NRGBA{240, 240, 240, 255})
				}); err != nil {
					return err
				}
				if n == 1 {
					var err error
					page, err = os.ReadFile(path)
					if err != nil {
						return err
					}
				}
			}
			return nil
		})
		if err := c.Convert(context.Background(), in, out, map[string]any{"size": edge}); err != nil {
			t.Fatal(err)
		}
		if len(*calls) != 3 || (*calls)[2].name != "pdftoppm" || scaleArg((*calls)[2].args) != strconv.Itoa(edge) {
			t.Fatalf("calls %+v", *calls)
		}
		for _, bad := range []string{"-f", "-l", "-singlefile"} {
			for _, arg := range (*calls)[2].args {
				if arg == bad {
					t.Fatalf("%s", bad)
				}
			}
		}
		assertWhiteSquare(t, out, edge)
		got, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(got, page) {
			t.Fatal("output is the raster")
		}
	}
}

func TestPandocRenderUndrawable(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.md")
	out := filepath.Join(dir, "out.png")
	if err := os.WriteFile(in, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	precreate(t, out)
	c, _ := newScripted(func(_ context.Context, name string, args []string) error {
		if err := scriptTouch(name, args); err != nil {
			return err
		}
		if name != "pdftoppm" {
			return nil
		}
		prefix := args[len(args)-1]
		white := color.NRGBA{255, 255, 255, 255}
		if err := writeSolid(prefix+"-1.png", 256, 256, white, nil); err != nil {
			return err
		}
		if err := os.WriteFile(prefix+"-2.png", []byte("not a png"), 0o600); err != nil {
			return err
		}
		return writeSolid(prefix+"-3.png", 256, 256, white, func(img *image.NRGBA) {
			img.SetNRGBA(1, 1, color.NRGBA{0, 0, 0, 255})
		})
	})
	if err := c.Convert(context.Background(), in, out, map[string]any{"size": 256}); err == nil {
		t.Fatal("nil error")
	}
	absentOutput(t, out)
}

func TestPandocRenderLayoutError(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.md")
	out := filepath.Join(dir, "out.png")
	if err := os.WriteFile(in, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	precreate(t, out)
	ppm := 0
	c, _ := newScripted(func(_ context.Context, name string, args []string) error {
		switch name {
		case "pandoc":
			return os.WriteFile(argAfter(args, "-o"), []byte("<html></html>\n"), 0o600)
		case "weasyprint":
			return errors.New("layout")
		case "pdftoppm":
			ppm++
			return nil
		default:
			return fmt.Errorf("unexpected %s", name)
		}
	})
	if err := c.Convert(context.Background(), in, out, nil); err == nil {
		t.Fatal("nil error")
	}
	if ppm != 0 {
		t.Fatalf("pdftoppm %d", ppm)
	}
	absentOutput(t, out)
}

func TestPandocRenderSize(t *testing.T) {
	accepted := []struct {
		opts map[string]any
		edge string
	}{
		{nil, "1280"},
		{map[string]any{}, "1280"},
		{map[string]any{"size": 256}, "256"},
		{map[string]any{"size": int32(256)}, "256"},
		{map[string]any{"size": int64(256)}, "256"},
		{map[string]any{"size": float64(256)}, "256"},
		{map[string]any{"size": 2048}, "2048"},
		{map[string]any{"size": int32(2048)}, "2048"},
		{map[string]any{"size": int64(2048)}, "2048"},
		{map[string]any{"size": float64(2048)}, "2048"},
	}
	for _, tc := range accepted {
		dir := t.TempDir()
		in := filepath.Join(dir, "in.md")
		out := filepath.Join(dir, "out.png")
		if err := os.WriteFile(in, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		c, calls := newScripted(func(_ context.Context, name string, args []string) error {
			if err := scriptTouch(name, args); err != nil {
				return err
			}
			if name != "pdftoppm" {
				return nil
			}
			edge, err := strconv.Atoi(scaleArg(args))
			if err != nil {
				return err
			}
			return contentPage(args[len(args)-1], edge)
		})
		if err := c.Convert(context.Background(), in, out, tc.opts); err != nil {
			t.Fatal(err)
		}
		if len(*calls) != 3 || scaleArg((*calls)[2].args) != tc.edge {
			t.Fatalf("opts %#v calls %+v", tc.opts, *calls)
		}
		assertNotAllWhite(t, out)
	}
	rejected := []map[string]any{
		{"size": 255},
		{"size": 2049},
		{"size": 256.5},
		{"size": math.NaN()},
		{"size": math.Inf(1)},
		{"size": "256"},
		{"size": true},
		{"standalone": true},
		{"size": 256, "toc": true},
	}
	for _, opts := range rejected {
		dir := t.TempDir()
		in := filepath.Join(dir, "in.md")
		out := filepath.Join(dir, "out.png")
		if err := os.WriteFile(in, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		precreate(t, out)
		n := 0
		c := PandocRenderConverter{PandocConverter: PandocConverter{run: func(context.Context, string, ...string) error {
			n++
			return errors.New("ran")
		}}}
		if err := c.Convert(context.Background(), in, out, opts); err == nil {
			t.Fatalf("accepted %#v", opts)
		}
		if n != 0 {
			t.Fatalf("runner %d for %#v", n, opts)
		}
		absentOutput(t, out)
	}
}

func TestPandocRenderCeiling(t *testing.T) {
	if DefaultMaxInputBytes != 3145728 {
		t.Fatal(DefaultMaxInputBytes)
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "in.md")
	out := filepath.Join(dir, "out.png")
	f, err := os.Create(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(3145729); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	precreate(t, out)
	n := 0
	c := PandocRenderConverter{PandocConverter: PandocConverter{run: func(context.Context, string, ...string) error {
		n++
		return errors.New("ran")
	}}}
	err = c.Convert(context.Background(), in, out, nil)
	if !errors.Is(err, ErrPandocInputTooLarge) {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("runner %d", n)
	}
	absentOutput(t, out)
}

func TestPandocRenderSevenSources(t *testing.T) {
	sources := []struct {
		ext    string
		reader string
	}{
		{"md", "markdown-raw_html-raw_tex"},
		{"rst", "rst"},
		{"org", "org"},
		{"latex", "latex-raw_tex"},
		{"html", "html-raw_html-raw_tex"},
		{"docx", "docx"},
		{"epub", "epub-raw_html-raw_tex"},
	}
	for _, src := range sources {
		if src.reader != pandocReaderSpec(src.ext) {
			t.Fatalf("%s reader %s", src.ext, pandocReaderSpec(src.ext))
		}
		dir := t.TempDir()
		in := filepath.Join(dir, "in."+src.ext)
		out := filepath.Join(dir, "out.png")
		if err := os.WriteFile(in, []byte("hello\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		c, calls := newScripted(func(_ context.Context, name string, args []string) error {
			if err := scriptTouch(name, args); err != nil {
				return err
			}
			if name == "pdftoppm" {
				return contentPage(args[len(args)-1], 256)
			}
			return nil
		})
		if err := c.Convert(context.Background(), in, out, map[string]any{"size": 256}); err != nil {
			t.Fatal(err)
		}
		if len(*calls) != 3 || (*calls)[0].name != "pandoc" || (*calls)[1].name != "weasyprint" || (*calls)[2].name != "pdftoppm" {
			t.Fatalf("%s calls %+v", src.ext, *calls)
		}
		args := (*calls)[0].args
		if argAfter(args, "-f") != src.reader || argAfter(args, "-t") != "html" {
			t.Fatalf("%s args %v", src.ext, args)
		}
		if !containsArg(args, "--sandbox") || !containsArg(args, "--standalone") {
			t.Fatalf("%s args %v", src.ext, args)
		}
		n, media := countExtract(args)
		if n != 1 || !pandocInside(media, dir) {
			t.Fatalf("%s extract %d %s", src.ext, n, media)
		}
		forbidRenderFlags(t, args)
		if src.ext == "txt" {
			t.Fatal("txt")
		}
	}
}

func TestPandocRenderExtractMedia(t *testing.T) {
	if _, err := exec.LookPath("pandoc"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	pixel := filepath.Join(dir, "pixel.png")
	if err := writeSolid(pixel, 1, 1, color.NRGBA{255, 0, 0, 255}, nil); err != nil {
		t.Fatal(err)
	}
	md := filepath.Join(dir, "pixel.md")
	if err := os.WriteFile(md, []byte(fmt.Sprintf("![](%s)\n", pixel)), 0o600); err != nil {
		t.Fatal(err)
	}
	docx := filepath.Join(dir, "in.docx")
	cmd := exec.Command("pandoc", "-f", "markdown", "-t", "docx", md, "-o", docx)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docx %v %s", err, out)
	}
	out := filepath.Join(dir, "out.png")
	saw := false
	c := PandocRenderConverter{PandocConverter: PandocConverter{run: func(ctx context.Context, name string, args ...string) error {
		switch name {
		case "pandoc":
			cmd := exec.CommandContext(ctx, "pandoc", args...)
			outBytes, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("%w %s", err, outBytes)
			}
			return nil
		case "weasyprint":
			if err := requireJobPNG(dir, args[len(args)-2]); err != nil {
				return err
			}
			saw = true
			return os.WriteFile(args[len(args)-1], []byte("%PDF-1.4\n"), 0o600)
		case "pdftoppm":
			return contentPage(args[len(args)-1], 256)
		default:
			return fmt.Errorf("unexpected %s", name)
		}
	}}}
	if err := c.Convert(context.Background(), docx, out, map[string]any{"size": 256}); err != nil {
		t.Fatal(err)
	}
	if !saw {
		t.Fatal("weasyprint was not called")
	}
	assertNotAllWhite(t, out)
}

func requireJobPNG(jobDir, htmlPath string) error {
	body, err := os.ReadFile(htmlPath)
	if err != nil {
		return err
	}
	srcs := pandocImgSrcs(string(body))
	if len(srcs) == 0 {
		return fmt.Errorf("missing src")
	}
	for _, src := range srcs {
		if pandocRemoteSrc(src) {
			continue
		}
		resolved, err := pandocResolveImage(src, filepath.Dir(htmlPath))
		if err != nil {
			return err
		}
		if !pandocInside(resolved, jobDir) {
			return fmt.Errorf("src outside %s", resolved)
		}
		f, err := os.Open(resolved)
		if err != nil {
			return err
		}
		magic := make([]byte, 8)
		_, err = f.Read(magic)
		_ = f.Close()
		if err != nil {
			return err
		}
		if !bytes.Equal(magic, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
			return fmt.Errorf("not a png %s", resolved)
		}
		return nil
	}
	return fmt.Errorf("missing src")
}

func TestPandocRenderGrants(t *testing.T) {
	base, err := os.MkdirTemp("/var/tmp", "pandoc-render-grant-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	bin := filepath.Join(base, "bin")
	job := filepath.Join(base, "job")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(job, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pandoc", "weasyprint", "pdftoppm"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	c := PandocRenderConverter{}
	c.sandbox = newEngineSandbox([]string{"/usr/local/bin/pandoc-worker", "exec"})
	ex, ok := c.layoutExec(job)
	if !ok {
		t.Fatal("unconfined")
	}
	spec := strings.Join(ex.spec, "\n")
	for _, want := range []string{
		"rw:" + job,
		"ro:/lib",
		"ro:/usr/lib",
		"ro:/usr/share/pandoc",
		"ro:/usr/bin/python3",
		"ro:/usr/bin/python3.11",
		"ro:/etc/ld.so.cache",
		"ro:/etc/localtime",
		"ro:/etc/fonts",
		"ro:/usr/share/fonts",
		"ro:/usr/share/fontconfig",
		"ro:" + filepath.Join(bin, "pandoc"),
		"ro:" + filepath.Join(bin, "weasyprint"),
		"ro:" + filepath.Join(bin, "pdftoppm"),
	} {
		if !strings.Contains(spec, want) {
			t.Errorf("missing %s in %s", want, spec)
		}
	}
	for _, bad := range []string{"rw:/tmp", "ro:/tmp", "/usr/lib/locale", "/usr/share/poppler"} {
		if strings.Contains(spec, bad) {
			t.Fatalf("spec contains %s: %s", bad, spec)
		}
	}
	if strings.Join(pandocReadOnly, "\n") != "/lib\n/usr/share/pandoc\n/etc/ld.so.cache\n/etc/localtime" {
		t.Fatalf("read only %v", pandocReadOnly)
	}
	if strings.Join(pandocEngines, "\n") != "pandoc" {
		t.Fatalf("engines %v", pandocEngines)
	}
}

func TestPandocRenderZeroPaddedPages(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.md")
	out := filepath.Join(dir, "out.png")
	if err := os.WriteFile(in, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	white := color.NRGBA{255, 255, 255, 255}
	c, calls := newScripted(func(_ context.Context, name string, args []string) error {
		if err := scriptTouch(name, args); err != nil {
			return err
		}
		if name != "pdftoppm" {
			return nil
		}
		prefix := args[len(args)-1]
		if err := writeSolid(prefix+"-01.png", 256, 256, white, nil); err != nil {
			return err
		}
		return writeSolid(prefix+"-02.png", 256, 256, white, func(img *image.NRGBA) {
			img.SetNRGBA(8, 8, color.NRGBA{0, 0, 0, 255})
		})
	})
	if err := c.Convert(context.Background(), in, out, map[string]any{"size": 256}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 3 {
		t.Fatalf("calls %d", len(*calls))
	}
	assertNotAllWhite(t, out)

	gap := t.TempDir()
	gapIn := filepath.Join(gap, "in.md")
	gapOut := filepath.Join(gap, "out.png")
	if err := os.WriteFile(gapIn, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	precreate(t, gapOut)
	gapConv, _ := newScripted(func(_ context.Context, name string, args []string) error {
		if err := scriptTouch(name, args); err != nil {
			return err
		}
		if name != "pdftoppm" {
			return nil
		}
		prefix := args[len(args)-1]
		if err := writeSolid(prefix+"-01.png", 256, 256, white, nil); err != nil {
			return err
		}
		return writeSolid(prefix+"-03.png", 256, 256, white, func(img *image.NRGBA) {
			img.SetNRGBA(8, 8, color.NRGBA{0, 0, 0, 255})
		})
	})
	if err := gapConv.Convert(context.Background(), gapIn, gapOut, map[string]any{"size": 256}); err == nil {
		t.Fatal("gap accepted")
	}
	absentOutput(t, gapOut)

	dup := t.TempDir()
	prefix := filepath.Join(dup, "pages")
	if err := writeSolid(prefix+"-1.png", 8, 8, white, nil); err != nil {
		t.Fatal(err)
	}
	if err := writeSolid(prefix+"-01.png", 8, 8, white, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := pandocRenderPages(prefix); err == nil {
		t.Fatal("duplicate accepted")
	}
}

func TestPandocRenderInheritsInputLimit(t *testing.T) {
	prevLimit := live.MaxInputBytes
	prevRun := live.run
	prevSandbox := live.sandbox
	t.Cleanup(func() {
		live.MaxInputBytes = prevLimit
		live.run = prevRun
		live.sandbox = prevSandbox
	})
	live.sandbox = nil
	var calls int
	live.run = func(context.Context, string, ...string) error {
		calls++
		return errors.New("stop")
	}

	dir := t.TempDir()
	in := filepath.Join(dir, "in.md")
	out := filepath.Join(dir, "out.png")
	f, err := os.Create(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(101); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	precreate(t, out)
	live.MaxInputBytes = 100
	err = (PandocRenderConverter{}).Convert(context.Background(), in, out, nil)
	if !errors.Is(err, ErrPandocInputTooLarge) || calls != 0 {
		t.Fatalf("err %v calls %d", err, calls)
	}
	absentOutput(t, out)

	if err := fTruncate(in, 3145729); err != nil {
		t.Fatal(err)
	}
	precreate(t, out)
	live.MaxInputBytes = 4000000
	calls = 0
	err = (PandocRenderConverter{}).Convert(context.Background(), in, out, nil)
	if errors.Is(err, ErrPandocInputTooLarge) || calls != 1 {
		t.Fatalf("err %v calls %d", err, calls)
	}
	absentOutput(t, out)
}

func fTruncate(path string, size int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
