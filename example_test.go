package coffee_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	coffee "github.com/vitJR1/coffee-templater.go"
)

func Example() {
	ctx := context.Background()
	template, _ := os.ReadFile("testdata/conformance/fixtures/invoice.docx")

	type Item struct {
		Name  string         `json:"name"`
		Qty   int            `json:"qty"`
		Price coffee.Decimal `json:"price"`
	}
	data := map[string]any{
		"number": 42,
		"client": map[string]any{"name": "Ann"},
		"items":  []Item{{"Coffee", 2, "10.50"}, {"Tea", 1, "4"}},
		"paid":   true,
	}

	templater := coffee.New(coffee.Options{
		Filters: map[string]coffee.Filter{
			"upper": func(v any, _ ...any) (any, error) { return strings.ToUpper(fmt.Sprint(v)), nil },
		},
	})
	doc, err := templater.Render(ctx, template, data)
	if err != nil {
		panic(err)
	}
	text, _ := coffee.ExtractText(ctx, doc)
	fmt.Println(strings.Split(text, "\n")[0])
	// Output: Invoice 42
}

func ExampleTemplater_Compile() {
	ctx := context.Background()
	template, _ := os.ReadFile("testdata/conformance/fixtures/report.xlsx")

	tpl, err := coffee.New(coffee.Options{}).Compile(ctx, template)
	if err != nil {
		panic(err)
	}
	defer tpl.Close()

	for _, title := range []string{"Q1", "Q2"} {
		doc, err := tpl.Render(ctx, map[string]any{"title": title, "items": []any{}})
		if err != nil {
			panic(err)
		}
		model, _ := coffee.Parse(ctx, doc)
		fmt.Println(model.Sheets[0].Rows[0].Cells[0].Value)
	}
	// Output:
	// Report: Q1
	// Report: Q2
}

func ExampleTemplate_Inspect() {
	ctx := context.Background()
	template, _ := os.ReadFile("testdata/conformance/fixtures/invoice.docx")

	res, err := coffee.New(coffee.Options{}).Inspect(ctx, template)
	if err != nil {
		panic(err)
	}
	fmt.Println(res.Variables[:3])
	// Output: [number client.name items[].name]
}

func ExampleError() {
	ctx := context.Background()
	template, _ := os.ReadFile("testdata/conformance/fixtures/broken.docx")

	_, err := coffee.New(coffee.Options{}).Compile(ctx, template)
	var e *coffee.Error
	if errors.As(err, &e) {
		for _, p := range e.Problems {
			fmt.Println(p.Code, p.Tag)
		}
	}
	fmt.Println(errors.Is(err, coffee.ErrUnknownFilter))
	// Output:
	// unclosed_section {{#items}}
	// unknown_filter {{company | nope}}
	// true
}

func ExampleBuild() {
	ctx := context.Background()
	sheet := coffee.SheetFromArrays("Prices", [][]any{
		{"Name", "Price"},
		{"Coffee", 10.5},
		{"Tea", 4},
	})
	doc, err := coffee.Build(ctx, &coffee.Document{Format: coffee.Xlsx, Sheets: []coffee.XlsxSheet{sheet}}, nil)
	if err != nil {
		panic(err)
	}
	text, _ := coffee.ExtractText(ctx, doc)
	fmt.Println(text)
	// Output:
	// Prices
	// Name	Price
	// Coffee	10.5
	// Tea	4
}
