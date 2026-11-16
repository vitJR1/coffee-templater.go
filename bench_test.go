package coffee

import (
	"context"
	"testing"
)

func BenchmarkNewEngine(b *testing.B) {
	ctx := context.Background()
	for i := 0; i < b.N; i++ {
		e, err := NewEngine(ctx, EngineConfig{MaxInstances: 1})
		if err != nil {
			b.Fatal(err)
		}
		e.Close(ctx)
	}
}

func BenchmarkRenderDocx(b *testing.B) {
	ctx := context.Background()
	tpl, err := New(Options{}).Compile(ctx, readFileB(b, invoice))
	if err != nil {
		b.Fatal(err)
	}
	defer tpl.Close()
	data := map[string]any{"number": 1, "client": map[string]any{"name": "Ann"},
		"items": []any{map[string]any{"name": "Coffee", "qty": 2, "price": 10.5}}}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := tpl.Render(ctx, data); err != nil {
				b.Fatal(err)
			}
		}
	})
}
