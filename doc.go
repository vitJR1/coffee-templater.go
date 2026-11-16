// Package coffee is a templating engine for Word (.docx), Excel (.xlsx) and PowerPoint (.pptx)
// files, plus a document model to parse Office files and build them back.
//
// It runs coffee-templater-engine, a Rust core compiled to WebAssembly and embedded in this
// package, with the pure-Go wazero runtime: no cgo, no external files. The same engine backs the
// JavaScript library, so documents render byte for byte the same everywhere.
//
// Templates are ordinary Office files with tags in the text:
//
//	Invoice {{number}} for {{client.name}}
//	{{#items}}- {{name}}: {{price | number: 2}}{{/items}}
//	{{#if paid}}Paid{{else}}Due{{/if}}
//	{{%logo}}
//
// Render a template once:
//
//	doc, err := coffee.New(coffee.Options{}).Render(ctx, template, data)
//
// or compile it once and render it many times (safe for concurrent use):
//
//	tpl, err := coffee.New(coffee.Options{}).Compile(ctx, template)
//	defer tpl.Close()
//	doc, err := tpl.Render(ctx, data)
//
// Data is converted like encoding/json does (struct tags included), plus time.Time and Date for
// dates, Decimal for exact decimals, []byte for binary data and Image for image tags.
//
// Errors from the engine are *Error values with a code that works with errors.Is:
//
//	if errors.Is(err, coffee.ErrUnclosedSection) { … }
//
// See https://github.com/vitJR1/coffee-templater-engine/blob/main/DOCS.md for the template
// language, filters and the document model.
package coffee
