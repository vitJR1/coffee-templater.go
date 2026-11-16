package coffee

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	invoice = "testdata/conformance/fixtures/invoice.docx"
	report  = "testdata/conformance/fixtures/report.xlsx"
	deck    = "testdata/conformance/fixtures/deck.pptx"
)

func encodeString(t *testing.T, v any) (string, [][]byte) {
	t.Helper()
	enc := encoder{}
	if err := enc.encode(v); err != nil {
		t.Fatal(err)
	}
	return enc.buf.String(), enc.blobs
}

// ------------------------------------------------------------------------------- values

func TestEncodeValues(t *testing.T) {
	type Inner struct {
		City string `json:"city"`
	}
	type Embedded struct {
		Promoted string
		Hidden   string `json:"name"` // hidden by Outer.Name
	}
	type Outer struct {
		Embedded
		Name    string         `json:"name"`
		Skip    string         `json:"-"`
		Empty   string         `json:"empty,omitempty"`
		Zero    time.Time      `json:"zero,omitzero"`
		Address *Inner         `json:"address"`
		Nil     *Inner         `json:"nil"`
		Tags    []string       `json:"tags"`
		Map     map[string]int `json:"map"`
		IntMap  map[int]bool   `json:"intMap"`
		private string
		Any     any             `json:"any"`
		Raw     json.RawMessage `json:"raw"`
		Num     json.Number     `json:"num"`
	}
	v := Outer{
		Embedded: Embedded{Promoted: "p", Hidden: "h"},
		Name:     "Ann", Skip: "x", Address: &Inner{"Paris"},
		Tags: []string{"a", "b"}, Map: map[string]int{"b": 2, "a": 1}, IntMap: map[int]bool{2: true, 1: false},
		private: "no", Any: 1.5, Raw: json.RawMessage(` {"x": [1]} `), Num: "12345678901234567890",
	}
	got, _ := encodeString(t, v)
	want := `{"Promoted":"p","name":"Ann","address":{"city":"Paris"},"nil":null,"tags":["a","b"],"map":{"a":1,"b":2},"intMap":{"1":false,"2":true},"any":1.5,"raw":{"x": [1]},"num":12345678901234567890}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestEncodeSpecialValues(t *testing.T) {
	moscow := time.FixedZone("MSK", 3*3600)
	ts := time.Date(2024, 5, 17, 10, 0, 0, 0, moscow)
	bi, _ := new(big.Int).SetString("123456789012345678901234567890", 10)
	cases := []struct {
		in   any
		want string
	}{
		{nil, `null`},
		{true, `true`},
		{"a\"b\\\n\u0001ü", `"a\"b\\\n\u0001ü"`},
		{10.5, `10.5`},
		{float32(0.1), `0.1`},
		{1e21, `1e+21`},
		{1e-7, `1e-7`},
		{math.NaN(), `null`},
		{math.Inf(1), `null`},
		{-3, `-3`},
		{uint64(math.MaxUint64), `18446744073709551615`},
		{ts, `{"$date":"2024-05-17T07:00:00.000Z"}`},
		{&ts, `{"$date":"2024-05-17T07:00:00.000Z"}`},
		{Date("2024-05-17"), `{"$date":"2024-05-17"}`},
		{DateOf(ts), `{"$date":"2024-05-17"}`},
		{DateTimeOf(ts), `{"$date":"2024-05-17T10:00:00.000"}`},
		{Decimal("1234.50"), `{"$decimal":"1234.50"}`},
		{bi, `123456789012345678901234567890`},
		{*bi, `123456789012345678901234567890`},
		{big.NewFloat(1.25), `{"$decimal":"1.25"}`},
		{big.NewRat(1, 4), `{"$decimal":"0.25"}`},
		{big.NewRat(8, 2), `4`},
		{map[string]any{"$date": "not a date"}, `{"$literal":{"$date":"not a date"}}`},
		{map[string]any{"$date": "x", "other": 1}, `{"$date":"x","other":1}`},
		{struct {
			D string `json:"$decimal"`
		}{"1"}, `{"$literal":{"$decimal":"1"}}`},
		{[]any{1, "a", nil}, `[1,"a",null]`},
		{[2]int{1, 2}, `[1,2]`},
		{[]int(nil), `null`},
		{map[string]int(nil), `null`},
		{func() {}, `null`},
		{textKey("k"), `"text:k"`},
		{map[textKey]int{"a": 1}, `{"a":1}`}, // string keys win over MarshalText, as in encoding/json
		{map[keyStruct]int{{1}: 1}, `{"k1":1}`},
	}
	for _, c := range cases {
		got, _ := encodeString(t, c.in)
		if got != c.want {
			t.Errorf("%#v: got %s, want %s", c.in, got, c.want)
		}
	}
}

type textKey string

func (k textKey) MarshalText() ([]byte, error) { return []byte("text:" + k), nil }

type keyStruct struct{ n int }

func (k keyStruct) MarshalText() ([]byte, error) { return []byte("k" + jsString(float64(k.n))), nil }

func TestEncodeTimeZone(t *testing.T) {
	ts := time.Date(2024, 5, 17, 23, 30, 0, 0, time.UTC)
	moscow := time.FixedZone("MSK", 3*3600)
	enc := encoder{loc: moscow}
	if err := enc.encode(ts); err != nil {
		t.Fatal(err)
	}
	if got := enc.buf.String(); got != `{"$date":"2024-05-18T02:30:00.000"}` {
		t.Errorf("got %s", got)
	}
}

func TestEncodeBlobs(t *testing.T) {
	img := Image{Data: []byte{1, 2, 3}, Width: 120}
	enc := encoder{blobBase: 1}
	if err := enc.encode(map[string]any{"logo": img, "raw": []byte("x")}); err != nil {
		t.Fatal(err)
	}
	want := `{"logo":{"data":{"$blob":1},"width":120},"raw":{"$blob":2}}`
	if got := enc.buf.String(); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if len(enc.blobs) != 2 || !bytes.Equal(enc.blobs[0], []byte{1, 2, 3}) || string(enc.blobs[1]) != "x" {
		t.Errorf("blobs %v", enc.blobs)
	}
}

func TestEncodeCycle(t *testing.T) {
	type node struct{ Next *node }
	n := &node{}
	n.Next = n
	enc := encoder{}
	if err := enc.encode(n); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected a cycle error, got %v", err)
	}
	m := map[string]any{}
	m["self"] = m
	if err := (&encoder{}).encode(m); err == nil {
		t.Fatal("expected a cycle error for a map")
	}
}

func TestDecodeValue(t *testing.T) {
	raw := json.RawMessage(`{"n":1.5,"d":{"$date":"2024-05-17"},"m":{"$decimal":"1.50"},"b":{"$blob":0},
		"b64":{"$base64":"data:image/png;base64,AQID"},"lit":{"$literal":{"$date":"x"}},"arr":[1,{"$decimal":"2"}],
		"two":{"$date":"x","y":1}}`)
	got, err := decodeValue(raw, [][]byte{[]byte("blob")})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"n": 1.5, "d": Date("2024-05-17"), "m": Decimal("1.50"), "b": []byte("blob"),
		"b64": []byte{1, 2, 3}, "lit": map[string]any{"$date": "x"}, "arr": []any{1.0, Decimal("2")},
		"two": map[string]any{"$date": "x", "y": 1.0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %#v\nwant %#v", got, want)
	}
	if _, err := decodeValue(json.RawMessage(`{"$blob":3}`), nil); err == nil {
		t.Error("expected an error for a missing blob")
	}
}

func TestDateTime(t *testing.T) {
	for s, want := range map[Date]time.Time{
		"2024-05-17":               time.Date(2024, 5, 17, 0, 0, 0, 0, time.UTC),
		"2024-05-17T10:00:00.000":  time.Date(2024, 5, 17, 10, 0, 0, 0, time.UTC),
		"2024-05-17T07:00:00.000Z": time.Date(2024, 5, 17, 7, 0, 0, 0, time.UTC),
	} {
		got, err := s.Time()
		if err != nil || !got.Equal(want) {
			t.Errorf("%s: %v %v", s, got, err)
		}
	}
	if _, err := Date("nope").Time(); err == nil {
		t.Error("expected an error")
	}
}

// ------------------------------------------------------------------------------- frames

func TestFrames(t *testing.T) {
	f := encodeFrame([]byte(`{"a":1}`), [][]byte{[]byte("xy"), {}})
	j, blobs, err := decodeFrame(f)
	if err != nil || string(j) != `{"a":1}` || len(blobs) != 2 || string(blobs[0]) != "xy" || len(blobs[1]) != 0 {
		t.Fatalf("round trip: %s %v %v", j, blobs, err)
	}
	for _, bad := range [][]byte{nil, []byte("CTF2\x00\x00\x00\x00\x00\x00\x00\x00"), f[:len(f)-1], f[:10]} {
		if _, _, err := decodeFrame(bad); err == nil {
			t.Errorf("expected an error for %q", bad)
		}
	}
	// a huge blob count must not allocate
	huge := append(encodeFrame([]byte(`{}`), nil)[:10], 0xff, 0xff, 0xff, 0xff)
	if _, _, err := decodeFrame(huge); err == nil {
		t.Error("expected an error for a huge blob count")
	}
}

// ------------------------------------------------------------------------------- templating

func TestRenderGoData(t *testing.T) {
	ctx := context.Background()
	type item struct {
		Name  string  `json:"name"`
		Qty   int     `json:"qty"`
		Price Decimal `json:"price"`
	}
	data := struct {
		Number  int    `json:"number"`
		Company string `json:"company"`
		Client  struct {
			Name string `json:"name"`
		} `json:"client"`
		Items []item  `json:"items"`
		Paid  bool    `json:"paid"`
		Notes string  `json:"notes"`
		Logo  *Image  `json:"logo"`
		Total float64 `json:"total"`
	}{Number: 42, Company: "ACME", Items: []item{{"Coffee", 2, "10.5"}, {"Tea", 1, "4"}}, Paid: true, Notes: "a\nb", Total: 25}
	data.Client.Name = "Иван"
	data.Logo = &Image{Data: readFile(t, "testdata/conformance/cases/docx-invoice-render/blobs/0-logo.png"), Width: 120, Height: 60}

	doc, err := New(Options{}).Render(ctx, readFile(t, invoice), data)
	if err != nil {
		t.Fatal(err)
	}
	text, err := ExtractText(ctx, doc)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"Invoice 42", "Client: Иван", "- Coffee: 10,50", "Status: paid", "Notes: a\nb"} {
		if !strings.Contains(text, s) {
			t.Errorf("missing %q in:\n%s", s, text)
		}
	}
	if _, ok := unzip(t, doc)["word/media/image_ct1.png"]; !ok {
		t.Error("the image is missing")
	}
}

func TestCallbacks(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	var tags []TagInfo
	var got []any
	tr := New(Options{
		Filters: map[string]Filter{
			"number": func(v any, args ...any) (any, error) {
				mu.Lock()
				got = append(got, v, args)
				mu.Unlock()
				return map[string]any{"v": v, "args": args}, nil
			},
			"upper": func(v any, _ ...any) (any, error) { panic("boom") },
		},
		NullValue: NullValue{Func: func(tag TagInfo) (any, error) {
			mu.Lock()
			tags = append(tags, tag)
			mu.Unlock()
			return "<" + tag.Expression + ">", nil
		}},
	})
	tpl, err := tr.Compile(ctx, readFile(t, invoice))
	if err != nil {
		t.Fatal(err)
	}
	defer tpl.Close()
	doc, err := tpl.Render(ctx, map[string]any{"items": []any{map[string]any{"name": "x", "price": Decimal("1.5")}}})
	if err != nil {
		t.Fatal(err)
	}
	text, _ := ExtractText(ctx, doc)
	if !reflect.DeepEqual(got, []any{Decimal("1.5"), []any{2.0}}) {
		t.Errorf("filter arguments: %#v", got)
	}
	if !strings.Contains(text, `- x: {"args":[2],"v":1.5}`) {
		t.Errorf("filter result:\n%s", text)
	}
	if !strings.Contains(text, "Invoice <number>") || len(tags) == 0 || tags[0].Part != "word/document.xml" {
		t.Errorf("null value: %v\n%s", tags, text)
	}

	// a panicking filter fails the render, and the engine keeps working
	_, err = New(Options{Filters: map[string]Filter{"number": func(any, ...any) (any, error) { panic("boom") }}}).
		Render(ctx, readFile(t, invoice), map[string]any{"items": []any{map[string]any{"price": 1}}})
	var ce *Error
	if !errors.As(err, &ce) || !errors.Is(err, ErrFilterFailed) || !strings.Contains(ce.Details["hostMessage"].(string), "boom") {
		t.Fatalf("expected filter_failed, got %v", err)
	}
	if _, err := tpl.Render(ctx, nil); err != nil {
		t.Fatal(err)
	}
}

func TestStringify(t *testing.T) {
	ctx := context.Background()
	tr := New(Options{Stringify: func(v any) (string, error) {
		if f, ok := v.(float64); ok {
			return "#" + jsString(f), nil
		}
		return "?", nil
	}})
	doc, err := tr.Render(ctx, readFile(t, invoice), map[string]any{"number": 7})
	if err != nil {
		t.Fatal(err)
	}
	if text, _ := ExtractText(ctx, doc); !strings.Contains(text, "Invoice #7") {
		t.Errorf("stringify:\n%s", text)
	}
}

func TestCompileErrors(t *testing.T) {
	_, err := New(Options{}).Compile(context.Background(), readFile(t, "testdata/conformance/fixtures/broken.docx"))
	var ce *Error
	if !errors.As(err, &ce) || len(ce.Problems) != 2 {
		t.Fatalf("got %v", err)
	}
	if !errors.Is(err, ErrUnclosedSection) || !errors.Is(err, ErrUnknownFilter) || errors.Is(err, ErrInternal) {
		t.Error("errors.Is does not match the problem codes")
	}
	if !strings.Contains(err.Error(), "(+1 more)") || !strings.Contains(err.Error(), "{{#items}}") {
		t.Errorf("message: %s", err)
	}
	if _, err := New(Options{Delimiters: Delimiters{"{{", "{{"}}).Compile(context.Background(), readFile(t, invoice)); !errors.Is(err, ErrInvalidOptions) {
		t.Errorf("expected invalid_options, got %v", err)
	}
	if _, err := New(Options{}).Compile(context.Background(), []byte("not a zip")); !errors.Is(err, ErrInvalidDocument) {
		t.Errorf("expected invalid_document, got %v", err)
	}
}

func TestInspect(t *testing.T) {
	res, err := New(Options{}).Inspect(context.Background(), readFile(t, invoice))
	if err != nil {
		t.Fatal(err)
	}
	if res.Format != Docx || len(res.Variables) == 0 || res.Schema["client"] == nil {
		t.Fatalf("%+v", res)
	}
	var section *Tag
	for i := range res.Tags {
		if res.Tags[i].Type == TagSection && res.Tags[i].Path == "items" {
			section = &res.Tags[i]
		}
	}
	if section == nil || section.Mode != ModeSection || len(section.Children) == 0 {
		t.Errorf("items section: %+v", section)
	}
}

func TestTemplateClose(t *testing.T) {
	ctx := context.Background()
	tpl, err := New(Options{}).Compile(ctx, readFile(t, invoice))
	if err != nil {
		t.Fatal(err)
	}
	tpl.Close()
	tpl.Close()
	if _, err := tpl.Render(ctx, nil); !errors.Is(err, errClosed) {
		t.Fatalf("expected errClosed, got %v", err)
	}
}

func TestConcurrentRender(t *testing.T) {
	ctx := context.Background()
	eng, err := NewEngine(ctx, EngineConfig{MaxInstances: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close(ctx)
	tr := New(Options{Engine: eng, Filters: map[string]Filter{"shout": func(v any, _ ...any) (any, error) { return v, nil }}})
	tpl, err := tr.Compile(ctx, readFile(t, report))
	if err != nil {
		t.Fatal(err)
	}
	defer tpl.Close()

	var wg sync.WaitGroup
	results := make([][]byte, 24)
	errs := make([]error, len(results))
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = tpl.Render(ctx, map[string]any{"title": "Q1", "items": []any{map[string]any{"name": "a", "qty": i}}})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("render %d: %v", i, err)
		}
	}
	// the same data gives the same bytes on every instance
	again, err := tpl.Render(ctx, map[string]any{"title": "Q1", "items": []any{map[string]any{"name": "a", "qty": 5}}})
	if err != nil || !bytes.Equal(again, results[5]) {
		t.Errorf("renders are not deterministic across instances: %v", err)
	}
	tpl.mu.Lock()
	n := len(tpl.handles)
	tpl.mu.Unlock()
	if n < 1 || n > 3 {
		t.Errorf("template compiled on %d instances, expected 1..3", n)
	}
}

func TestCancel(t *testing.T) {
	eng, err := NewEngine(context.Background(), EngineConfig{MaxInstances: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close(context.Background())

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(Options{Engine: eng}).Render(canceled, readFile(t, invoice), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	// cancelling while the engine runs interrupts it; the instance is replaced
	ctx, cancel := context.WithCancel(context.Background())
	tr := New(Options{Engine: eng, Filters: map[string]Filter{"number": func(v any, _ ...any) (any, error) {
		cancel()
		time.Sleep(10 * time.Millisecond)
		return v, nil
	}}})
	tpl, err := tr.Compile(context.Background(), readFile(t, invoice))
	if err != nil {
		t.Fatal(err)
	}
	defer tpl.Close()
	items := make([]any, 2000)
	for i := range items {
		items[i] = map[string]any{"name": "x", "price": i}
	}
	if _, err := tpl.Render(ctx, map[string]any{"items": items}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	// the template compiles again on the new instance
	if _, err := tpl.Render(context.Background(), map[string]any{"number": 1}); err != nil {
		t.Fatalf("render after cancel: %v", err)
	}
}

// ------------------------------------------------------------------------------- model

// The typed model must carry everything the engine returns, so that Parse → Build is lossless.
func TestModelLossless(t *testing.T) {
	ctx := context.Background()
	eng, err := DefaultEngine()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{invoice, report, deck} {
		doc := readFile(t, path)
		raw, err := eng.documentOp(ctx, "parse", doc, nil)
		if err != nil {
			t.Fatal(err)
		}
		var engineModel struct {
			Model any `json:"model"`
		}
		if err := json.Unmarshal(raw, &engineModel); err != nil {
			t.Fatal(err)
		}
		typed, err := eng.Parse(ctx, doc)
		if err != nil {
			t.Fatal(err)
		}
		if diff := jsonDiff("", dropNulls(engineModel.Model), generic(t, typed)); diff != "" {
			t.Errorf("%s: the typed model differs from the engine model: %s", path, diff)
		}
		built, err := Build(ctx, typed, doc)
		if err != nil {
			t.Fatalf("%s: build: %v", path, err)
		}
		before, _ := ExtractText(ctx, doc)
		after, _ := ExtractText(ctx, built)
		if before != after {
			t.Errorf("%s: text changed after a round trip:\n%s\n---\n%s", path, before, after)
		}
	}
}

func TestBuildNew(t *testing.T) {
	ctx := context.Background()
	sheet := SheetFromArrays("Data", [][]any{{"Name", "Qty"}, {"Tea", 2}, {"Coffee", 3.5}, {nil, true}})
	doc, err := Build(ctx, &Document{Format: Xlsx, Sheets: []XlsxSheet{sheet}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(ctx, doc)
	if err != nil {
		t.Fatal(err)
	}
	if f, _ := DetectFormat(ctx, doc); f != Xlsx {
		t.Errorf("format %s", f)
	}
	rows := SheetToArrays(m.Sheets[0])
	want := [][]any{{"Name", "Qty"}, {"Tea", 2.0}, {"Coffee", 3.5}, {nil, true}}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("got %v", rows)
	}
	objs := SheetToObjects(m.Sheets[0], 1)
	if len(objs) != 3 || objs[1]["Name"] != "Coffee" || objs[2]["column1"] != nil && objs[2]["Name"] != nil {
		t.Errorf("objects %v", objs)
	}

	para := DocxBlock{Type: "paragraph", Children: []DocxInline{{Type: "run", Text: "Hello", DocxRunStyle: DocxRunStyle{Bold: Ptr(true), Underline: &Underline{Type: "double"}}}}}
	doc, err = Build(ctx, &Document{Format: Docx, Parts: []DocxPart{{Path: "word/document.xml", Kind: "body", Blocks: []DocxBlock{para}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if text, _ := ExtractText(ctx, doc); text != "Hello" {
		t.Errorf("text %q", text)
	}
	if _, err := Build(ctx, &Document{Format: Pptx}, nil); err == nil {
		t.Error("pptx without a base must fail")
	}
}

func TestEngineInfo(t *testing.T) {
	eng, err := DefaultEngine()
	if err != nil {
		t.Fatal(err)
	}
	info, err := eng.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSuffix(EngineVersion, "-local") != info.CoreVersion || info.ABIVersion != supportedABI || info.ModelVersion != ModelVersion {
		t.Errorf("%+v", info)
	}
}

func dropNulls(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if val == nil {
				delete(x, k)
			} else {
				x[k] = dropNulls(val)
			}
		}
	case []any:
		for i := range x {
			x[i] = dropNulls(x[i])
		}
	}
	return v
}

func jsonDiff(path string, a, b any) string {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			return path + ": not an object"
		}
		for k := range x {
			if d := jsonDiff(path+"."+k, x[k], y[k]); d != "" {
				return d
			}
		}
		for k := range y {
			if _, ok := x[k]; !ok {
				return path + "." + k + ": extra"
			}
		}
		return ""
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return path + ": arrays differ"
		}
		for i := range x {
			if d := jsonDiff(path+"["+jsString(float64(i))+"]", x[i], y[i]); d != "" {
				return d
			}
		}
		return ""
	}
	if !reflect.DeepEqual(a, b) {
		return path + ": " + jsString(a) + " != " + jsString(b)
	}
	return ""
}
