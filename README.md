# coffee-templater for Go

Templating engine for **Word (.docx)**, **Excel (.xlsx)** and **PowerPoint (.pptx)**, plus a document
model to parse Office files and build them back.

Powered by [coffee-templater-engine](https://github.com/vitJR1/coffee-templater-engine), a Rust core
compiled to WebAssembly. The engine is embedded in this module and runs on the pure-Go
[wazero](https://wazero.io) runtime: **no cgo and no files to ship**. It is the same engine as in
[coffee-templater.js](https://github.com/vitJR1/coffee-templater.js), so a document rendered from Go is
byte for byte identical to the same document rendered from Node.js.

- Variables, loops, conditions, `else`, filters, images
- Configurable delimiters (`{{x}}`, `${x}`, `[[x]]`, …) and tag syntax
- Tags split by Word into several runs (`{{na` + `me}}`) are handled transparently
- Loops repeat inline text, paragraphs, table rows (Word, PowerPoint) or sheet rows (Excel)
- Excel: numbers stay numbers, rows below a loop shift, formulas and merged cells follow
- Custom filters written in Go
- `Inspect` lists the variables of a template and returns a sample data shape
- `Parse` → Go structs → `Build`: edit documents without losing formatting
- Safe for untrusted uploads: limits against zip bombs, deep XML and runaway loops
- Safe for concurrent use: a pool of engine instances; cancelling a `context` interrupts a render

```bash
go get github.com/vitJR1/coffee-templater.go
```

Requires Go 1.23 or later. A detailed guide in Russian is in [DOCS.md](DOCS.md).

## Quick start

```go
import coffee "github.com/vitJR1/coffee-templater.go"

templater := coffee.New(coffee.Options{})

template, _ := os.ReadFile("invoice.docx")
doc, err := templater.Render(ctx, template, map[string]any{
	"number": 42,
	"client": map[string]any{"name": "Ivan Petrov"},
	"items": []map[string]any{
		{"name": "Coffee", "qty": 2, "price": 10.5},
		{"name": "Tea", "qty": 1, "price": 4},
	},
	"paid": false,
})
if err != nil {
	return err
}
os.WriteFile("invoice-42.docx", doc, 0o644)
```

The same call works for `.xlsx` and `.pptx`: the format is detected from the file.

Compile once and render many times. A `*Template` is safe for concurrent use:

```go
tpl, err := templater.Compile(ctx, template)
if err != nil {
	return err
}
defer tpl.Close()

for _, inv := range invoices {
	doc, err := tpl.Render(ctx, inv)
	// …
}
```

## Template syntax

| Syntax | Meaning |
| --- | --- |
| `{{name}}` | variable |
| `{{client.address.city}}`, `{{items[0].name}}`, `{{map["key with spaces"]}}` | paths |
| `{{price \| number: 2}}` | filter with arguments |
| `{{#items}}…{{/items}}` | loop over an array, enter an object, or render when truthy |
| `{{#each items}}…{{/each}}` | explicit loop (arrays and objects, `@key` for objects) |
| `{{#if paid}}…{{else}}…{{/if}}` | condition |
| `{{#unless paid}}…{{/unless}}` | negative condition |
| `{{^items}}…{{/items}}` | inverted section: rendered when the value is empty or falsy |
| `{{/}}` | closes the innermost section |
| `{{%logo}}` | image |
| `\{{text}}` | literal `{{text}}` |

Inside loops: `@index`, `@number` (1-based), `@first`, `@last`, `@length`, `@key`; `.` / `this` is the
current item and `../name` reaches the outer scope.

Built-in filters: `upper lower capitalize trim replace truncate prepend append default`,
`number fixed round currency percent date`, `join length first last reverse sort where slice json`,
`eq ne gt gte lt lte not empty and or contains yesno`.

The complete reference (placement rules for loops, every filter, Excel specifics) is in the engine's
[DOCS.md](https://github.com/vitJR1/coffee-templater-engine/blob/main/DOCS.md).

## Data

Data is converted the way `encoding/json` does it: structs with their `json` tags (`omitempty`,
`omitzero`, `-`, embedded structs), maps, slices, `json.Marshaler` and `encoding.TextMarshaler`. On top
of that:

| Go value | In the template |
| --- | --- |
| `time.Time` | a date: an absolute instant printed in UTC, or wall-clock time in `Options.TimeZone` |
| `coffee.Date("2024-05-17")` | a naive date, printed as written (as in Excel) |
| `coffee.Decimal("1234.50")` | an exact decimal; `number`, `fixed`, `round` and `currency` round it exactly |
| `*big.Int`, `json.Number` | a number without precision loss |
| `*big.Float`, `*big.Rat` | an exact decimal |
| `[]byte` | binary data, sent to the engine as is (not base64) |
| `coffee.Image{…}` | an image for `{{%tag}}` |

Map keys are sorted, so the same data always gives the same document.

```go
type Item struct {
	Name  string         `json:"name"`
	Price coffee.Decimal `json:"price"`
}

data := struct {
	Date  time.Time     `json:"date"`
	Items []Item        `json:"items"`
	Logo  *coffee.Image `json:"logo"` // nil removes the image tag
}{
	Date:  time.Now(),
	Items: []Item{{"Coffee", "10.50"}},
	Logo:  &coffee.Image{Data: png, Width: 120}, // pixels at 96 dpi; height follows the aspect ratio
}
```

Images: PNG, JPEG, GIF, BMP and SVG are detected from the bytes; other formats (`webp`, `tiff`, `emf`,
…) need `Extension`. In Word the picture is inline at the tag. In PowerPoint the tag must be the only
content of a text box, which is replaced by the picture fitted into it. Excel has no image tags yet.

## Options

```go
coffee.New(coffee.Options{
	Delimiters: coffee.Delimiters{Start: "${", End: "}"}, // default {{ }}
	EscapeChar: `\`,                                      // NoEscape: true disables escaping
	Syntax:     coffee.Syntax{Section: "@", Else: "otherwise"},
	Strict:     true,                    // missing values fail with ErrMissingValue
	NullValue:  coffee.NullValue{Mode: coffee.NullText, Text: "—"},
	DateFormat: "DD.MM.YYYY",            // default format of dates printed without a filter
	TimeZone:   moscow,                  // *time.Location for time.Time values
	Limits:     coffee.Limits{MaxLoopIterations: 10_000},
})
```

`NullValue.Mode` is `NullEmpty` (default), `NullText`, `NullKeep` (print the original tag) or
`NullError`; `NullValue.Func` decides per tag instead. `Stringify` converts every non-string value to
text. `NoLinebreaks` keeps `\n` in values instead of turning it into a line break.

## Custom filters

```go
templater := coffee.New(coffee.Options{
	Filters: map[string]coffee.Filter{
		// {{ total | money: "RUB" }}
		"money": func(value any, args ...any) (any, error) {
			amount, ok := value.(float64)
			if !ok {
				return nil, fmt.Errorf("money: %v is not a number", value)
			}
			return fmt.Sprintf("%.2f %v", amount, args[0]), nil
		},
	},
})
```

Filters receive values as `map[string]any`, `[]any`, `float64`, `string`, `bool`, `nil`,
`coffee.Date`, `coffee.Decimal` and `[]byte`, and may return any value that could appear in data, so
they work in conditions and loops too: `{{#each items | onlyActive}}`. A filter with the name of a
built-in one replaces it. An error or a panic fails the render with `ErrFilterFailed`, and the message
is in `Details["hostMessage"]`.

A filter must not render with the same `Engine` while all of its instances are busy: a render that
waits for a free instance inside a filter would wait forever.

## Errors

The engine reports `*coffee.Error`. Compilation reports every mistake in the template at once:

```go
tpl, err := templater.Compile(ctx, template)
var e *coffee.Error
if errors.As(err, &e) {
	for _, p := range e.Problems {
		fmt.Println(p.Code, p.Tag, p.Part) // unclosed_section {{#items}} word/document.xml
	}
}
if errors.Is(err, coffee.ErrUnknownFilter) { … } // matches any of the problems
```

Template codes: `ErrUnclosedTag`, `ErrUnopenedTag`, `ErrUnclosedSection`, `ErrUnmatchedClose`,
`ErrInvalidExpression`, `ErrUnknownFilter`, `ErrFilterFailed`, `ErrMissingValue`, `ErrInvalidSection`,
`ErrInvalidImage`, `ErrUnsupported`. Engine codes: `ErrInvalidRequest`, `ErrInvalidOptions`,
`ErrInvalidDocument`, `ErrUnsupportedFormat`, `ErrInvalidModel`, `ErrUnknownTemplate`,
`ErrLimitExceeded`, `ErrHostError`, `ErrInternal`.

A cancelled or expired `ctx` returns `ctx.Err()`.

## Inspecting templates

```go
res, err := templater.Inspect(ctx, template)
res.Variables // [number client.name items[].name items[].price paid logo …]
res.Schema    // map[client:map[name:] items:[map[name: price:]] paid:true logo:<nil>]
res.Tags      // the tag tree: variables, images and sections with their children
```

Use it to build input forms or to validate data before rendering.

## Document model

`Parse` turns a file into Go structs; edit them and `Build` the file back, passing the original
file as the base. Everything the model does not cover (styles, images, fields, and so on) is kept as
raw XML, and fields are written back only when they change, so a round trip keeps the formatting.

```go
model, err := coffee.Parse(ctx, docx)
for i, block := range model.Parts[0].Blocks {
	if block.Type == "paragraph" {
		for j := range block.Children {
			run := &model.Parts[0].Blocks[i].Children[j]
			if run.Type == "run" {
				run.Text = strings.ReplaceAll(run.Text, "ACME", "Coffee Ltd")
				run.Bold = coffee.Ptr(true)
			}
		}
	}
}
out, err := coffee.Build(ctx, model, docx)
```

- Word: `Parts` (body, headers, footers) → blocks (paragraphs, tables, raw) → inlines (runs, breaks,
  hyperlinks, raw).
- Excel: `Sheets` → rows → cells with `Value` (string, float64, bool), `Formula`, `Style`, `Type`
  (`"date"`, `"error"`). Sheets can be renamed, reordered, removed and created. Helpers:
  `SheetToArrays`, `SheetToObjects`, `SheetFromArrays`, `ParseRef`, `ColumnToIndex`, `IndexToColumn`,
  `SerialToDate`, `DateToSerial`.
- PowerPoint: `Slides` → shapes (text, table, picture, …) → paragraphs → runs, plus speaker notes.
  Slides can be reordered and removed. A base file is required.

The base may be nil for docx and xlsx, so a document can also be built from scratch:

```go
sheet := coffee.SheetFromArrays("Prices", [][]any{{"Name", "Price"}, {"Coffee", 10.5}})
xlsx, err := coffee.Build(ctx, &coffee.Document{Format: coffee.Xlsx, Sheets: []coffee.XlsxSheet{sheet}}, nil)
```

`ExtractText` returns the plain text of a file and `DetectFormat` its format.

## Engine, concurrency and performance

An engine instance is single-threaded, so the package keeps a pool of instances: at most
`GOMAXPROCS` operations run in parallel by default. A `Template` compiles itself lazily on each
instance it runs on. Startup compiles the WebAssembly engine to machine code once per process (about
0.3 s); pass `CacheDir` to keep the compiled code on disk between runs.

```go
engine, err := coffee.NewEngine(ctx, coffee.EngineConfig{
	MaxInstances: 4,                      // default runtime.GOMAXPROCS(0)
	CacheDir:     "/var/cache/coffee",    // optional compilation cache
})
defer engine.Close(ctx)

templater := coffee.New(coffee.Options{Engine: engine})
model, err := engine.Parse(ctx, doc)
```

Without `Options.Engine`, `DefaultEngine()` is created on first use.

Cancelling the `ctx` of a running operation interrupts the engine; that instance is dropped and a new
one replaces it.

## Limits

Input is treated as untrusted. Defaults: 100 MiB input, 10 000 zip entries, 500 MiB uncompressed,
compression ratio 200, XML depth 256, 500 MiB output, 1 000 000 loop iterations and 1 000 000 filter
calls per render. Zero keeps a default; `coffee.Unlimited` removes a limit.

```go
coffee.New(coffee.Options{Limits: coffee.Limits{MaxInputBytes: 10 << 20, MaxLoopIterations: 10_000}})
coffee.Parse(ctx, upload, coffee.Limits{MaxEntries: 1000})
```

DTDs and external entities are never expanded; zip entries with unsafe paths are rejected.

## Development

```bash
go test ./...                     # unit tests and the engine's conformance suite
scripts/update-engine.sh 0.1.3    # embed another engine release (checks its sha256)
scripts/update-engine.sh --local ../coffee-templater-engine/dist/coffee_core.wasm
```

`testdata/conformance` is a copy of the engine's language-independent conformance suite. Every case
runs through the public Go API, and the sha256 of each rendered document must equal the one produced
by the engine's Rust and JavaScript runners.

## License

MIT
