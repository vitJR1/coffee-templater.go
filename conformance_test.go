package coffee

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The conformance suite of the engine (testdata/conformance, copied from
// coffee-templater-engine/conformance), run through the public Go API. The sha256 of every rendered
// document must match the one produced by the engine's own runners.

const conformanceDir = "testdata/conformance"

type conformanceCase struct {
	Description string                    `json:"description"`
	Op          string                    `json:"op"`
	Template    string                    `json:"template"`
	Options     json.RawMessage           `json:"options"`
	Limits      map[string]int64          `json:"limits"`
	HostFilters map[string]hostFilterSpec `json:"hostFilters"`
	Data        json.RawMessage           `json:"data"`
	Expect      conformanceExpect         `json:"expect"`
}

type hostFilterSpec struct {
	Type    string `json:"type"`
	Pattern string `json:"pattern"`
	Message string `json:"message"`
}

type pathCheck struct {
	Path   string `json:"path"`
	Equals any    `json:"equals"`
}

type conformanceExpect struct {
	Errors       []map[string]any `json:"errors"`
	Result       []pathCheck      `json:"result"`
	Text         *string          `json:"text"`
	TextContains []string         `json:"textContains"`
	Model        []pathCheck      `json:"model"`
	Parts        map[string]struct {
		Contains    []string `json:"contains"`
		NotContains []string `json:"notContains"`
	} `json:"parts"`
	Files struct {
		Contains    []string `json:"contains"`
		NotContains []string `json:"notContains"`
	} `json:"files"`
	SHA256 string `json:"sha256"`
}

func TestConformance(t *testing.T) {
	dirs, err := os.ReadDir(filepath.Join(conformanceDir, "cases"))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		dir := filepath.Join(conformanceDir, "cases", d.Name())
		t.Run(d.Name(), func(t *testing.T) {
			t.Parallel()
			runConformanceCase(t, dir)
		})
	}
}

func runConformanceCase(t *testing.T, dir string) {
	ctx := context.Background()
	var c conformanceCase
	readJSON(t, filepath.Join(dir, "case.json"), &c)
	eng, err := DefaultEngine()
	if err != nil {
		t.Fatal(err)
	}

	var template []byte
	if c.Template != "" {
		template = readFile(t, filepath.Join(conformanceDir, c.Template))
	}
	var blobs [][]byte
	if entries, err := os.ReadDir(filepath.Join(dir, "blobs")); err == nil {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		for _, n := range names {
			blobs = append(blobs, readFile(t, filepath.Join(dir, "blobs", n)))
		}
	}
	dataJSON := []byte(c.Data)
	if b, err := os.ReadFile(filepath.Join(dir, "data.json")); err == nil {
		dataJSON = b
	}
	data := goData(t, dataJSON, blobs)
	limits := limitsFromWire(c.Limits)

	var (
		result any
		doc    []byte
		opErr  error
	)
	switch c.Op {
	case "info":
		var info *Info
		info, opErr = eng.Info(ctx)
		result = info
	case "compile", "render", "inspect":
		tr := New(optionsFromCase(t, c))
		var tpl *Template
		tpl, opErr = tr.Compile(ctx, template)
		if opErr == nil {
			result = map[string]any{"format": tpl.Format(), "parts": tpl.Parts()}
			switch c.Op {
			case "render":
				doc, opErr = tpl.Render(ctx, data)
				if opErr == nil {
					// the one-shot path must give the same document
					once, err := tr.Render(ctx, template, data)
					if err != nil {
						t.Fatalf("Templater.Render: %v", err)
					}
					if !bytes.Equal(once, doc) {
						t.Errorf("Templater.Render differs from Template.Render")
					}
				}
			case "inspect":
				result, opErr = tpl.Inspect(ctx)
			}
			tpl.Close()
		}
	case "parse":
		result, opErr = eng.Parse(ctx, template, limits)
		if opErr == nil {
			result = map[string]any{"model": result}
		}
	case "extractText":
		var text string
		text, opErr = eng.ExtractText(ctx, template, limits)
		result = map[string]any{"text": text}
	case "detect":
		var f Format
		f, opErr = eng.DetectFormat(ctx, template, limits)
		result = map[string]any{"format": f}
	default:
		t.Fatalf("unknown op %q", c.Op)
	}

	ex := c.Expect
	if ex.Errors != nil {
		var ce *Error
		if !errors.As(opErr, &ce) {
			t.Fatalf("expected errors %v, got %v", ex.Errors, opErr)
		}
		got := generic(t, ce.Problems).([]any)
		for i, want := range ex.Errors {
			if i >= len(got) || !matches(got[i], want) {
				t.Errorf("error %d: expected %v, got %v", i, want, ce.Problems)
			}
			if code, _ := want["code"].(string); !errors.Is(opErr, ErrorCode(code)) {
				t.Errorf("errors.Is(err, %q) = false", code)
			}
		}
		return
	}
	if opErr != nil {
		t.Fatal(opErr)
	}
	res := generic(t, result)
	checkPaths(t, "result", res, ex.Result)
	switch c.Op {
	case "extractText":
		if ex.Text != nil && res.(map[string]any)["text"] != *ex.Text {
			t.Errorf("text: %q", res.(map[string]any)["text"])
		}
		return
	case "parse":
		checkPaths(t, "model", res.(map[string]any)["model"], ex.Model)
		return
	case "render":
	default:
		return
	}

	text := func() string {
		s, err := ExtractText(ctx, doc)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if ex.Text != nil && text() != *ex.Text {
		t.Errorf("text:\n%s", text())
	}
	for _, s := range ex.TextContains {
		if !strings.Contains(text(), s) {
			t.Errorf("text does not contain %q:\n%s", s, text())
		}
	}
	if ex.Model != nil {
		m, err := Parse(ctx, doc)
		if err != nil {
			t.Fatal(err)
		}
		checkPaths(t, "model", generic(t, m), ex.Model)
	}
	files := unzip(t, doc)
	for part, chk := range ex.Parts {
		xml, ok := files[part]
		if !ok {
			t.Errorf("missing part %s", part)
		}
		for _, s := range chk.Contains {
			if !strings.Contains(xml, s) {
				t.Errorf("%s does not contain %s", part, s)
			}
		}
		for _, s := range chk.NotContains {
			if strings.Contains(xml, s) {
				t.Errorf("%s contains %s", part, s)
			}
		}
	}
	for _, f := range ex.Files.Contains {
		if _, ok := files[f]; !ok {
			t.Errorf("missing file %s", f)
		}
	}
	for _, f := range ex.Files.NotContains {
		if _, ok := files[f]; ok {
			t.Errorf("unexpected file %s", f)
		}
	}
	if ex.SHA256 != "" {
		sum := sha256.Sum256(doc)
		if got := hex.EncodeToString(sum[:]); got != ex.SHA256 {
			t.Errorf("sha256 %s, expected %s", got, ex.SHA256)
		}
	}
}

// optionsFromCase maps the engine options of a case to Options, and its declarative host filters
// to Go filters.
func optionsFromCase(t *testing.T, c conformanceCase) Options {
	var w struct {
		Delimiters *Delimiters      `json:"delimiters"`
		Strict     bool             `json:"strict"`
		Syntax     *Syntax          `json:"syntax"`
		Limits     map[string]int64 `json:"limits"`
		Other      map[string]any   `json:"-"`
	}
	if len(c.Options) > 0 {
		if err := json.Unmarshal(c.Options, &w); err != nil {
			t.Fatal(err)
		}
		var all map[string]any
		_ = json.Unmarshal(c.Options, &all)
		for k := range all {
			switch k {
			case "delimiters", "strict", "syntax", "limits":
			default:
				t.Fatalf("option %q is not mapped by the test", k)
			}
		}
	}
	o := Options{Strict: w.Strict, Limits: limitsFromWire(w.Limits)}
	if w.Delimiters != nil {
		o.Delimiters = *w.Delimiters
	}
	if w.Syntax != nil {
		o.Syntax = *w.Syntax
	}
	if len(c.HostFilters) > 0 {
		o.Filters = map[string]Filter{}
		for name, spec := range c.HostFilters {
			o.Filters[name] = func(value any, args ...any) (any, error) {
				switch spec.Type {
				case "format":
					s := strings.ReplaceAll(spec.Pattern, "{value}", jsString(value))
					for i, a := range args {
						s = strings.ReplaceAll(s, fmt.Sprintf("{arg%d}", i), jsString(a))
					}
					return s, nil
				case "upper":
					return strings.ToUpper(jsString(value)), nil
				case "throw":
					return nil, errors.New(spec.Message)
				}
				return nil, errors.New("unknown host filter")
			}
		}
	}
	return o
}

func limitsFromWire(m map[string]int64) Limits {
	get := func(k string) int64 {
		v, ok := m[k]
		if !ok {
			return 0
		}
		if v == 0 {
			return Unlimited
		}
		return v
	}
	return Limits{
		MaxInputBytes: get("maxInputBytes"), MaxEntries: get("maxEntries"),
		MaxUncompressedBytes: get("maxUncompressedBytes"), MaxCompressionRatio: get("maxCompressionRatio"),
		MaxXMLDepth: get("maxXmlDepth"), MaxOutputBytes: get("maxOutputBytes"),
		MaxLoopIterations: get("maxLoopIterations"), MaxHostCalls: get("maxHostCalls"),
	}
}

// jsString is String(value) of JavaScript, as used by the reference runner.
func jsString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return string(appendFloat(nil, x, 64))
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// goData turns case data into Go values the way an application would pass them: number lexemes as
// json.Number, tagged values as Decimal, Date and []byte.
func goData(t *testing.T, raw []byte, blobs [][]byte) any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		t.Fatal(err)
	}
	var conv func(any) any
	conv = func(v any) any {
		switch x := v.(type) {
		case []any:
			for i := range x {
				x[i] = conv(x[i])
			}
		case map[string]any:
			if len(x) == 1 {
				for k, val := range x {
					switch k {
					case "$decimal":
						return Decimal(val.(string))
					case "$date":
						return Date(val.(string))
					case "$base64":
						b, err := decodeBase64(val.(string))
						if err != nil {
							t.Fatal(err)
						}
						return b
					case "$blob":
						i, _ := strconv.Atoi(val.(json.Number).String())
						return blobs[i]
					}
				}
			}
			for k := range x {
				x[k] = conv(x[k])
			}
		}
		return v
	}
	return conv(v)
}

// ------------------------------------------------------------------------------- helpers

func readFileB(tb testing.TB, path string) []byte {
	tb.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		tb.Fatal(err)
	}
	return b
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := json.Unmarshal(readFile(t, path), v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

// generic converts a Go value to plain JSON values (map[string]any, []any, float64…).
func generic(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

var segRe = regexp.MustCompile(`^([^\[]*)((?:\[\d+\])*)$`)
var idxRe = regexp.MustCompile(`\[(\d+)\]`)

func at(v any, path string) any {
	for _, seg := range strings.Split(path, ".") {
		if seg == "" {
			continue
		}
		m := segRe.FindStringSubmatch(seg)
		if m == nil {
			return nil
		}
		if m[1] != "" {
			obj, _ := v.(map[string]any)
			v = obj[m[1]]
		}
		for _, i := range idxRe.FindAllStringSubmatch(m[2], -1) {
			arr, _ := v.([]any)
			n, _ := strconv.Atoi(i[1])
			if n >= len(arr) {
				return nil
			}
			v = arr[n]
		}
	}
	return v
}

// matches compares like the reference runner: objects by the expected keys only, arrays by length
// and items.
func matches(actual, expected any) bool {
	switch e := expected.(type) {
	case []any:
		a, ok := actual.([]any)
		if !ok || len(a) != len(e) {
			return false
		}
		for i := range e {
			if !matches(a[i], e[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		a, ok := actual.(map[string]any)
		if !ok {
			return false
		}
		for k, ev := range e {
			if !matches(a[k], ev) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(actual, expected)
}

func checkPaths(t *testing.T, what string, root any, checks []pathCheck) {
	t.Helper()
	for _, c := range checks {
		if got := at(root, c.Path); !matches(got, c.Equals) {
			t.Errorf("%s %s: expected %v, got %v", what, c.Path, c.Equals, got)
		}
	}
}

func unzip(t *testing.T, doc []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(doc), int64(len(doc)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = string(b)
	}
	return out
}
