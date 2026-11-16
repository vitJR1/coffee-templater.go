package coffee

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"time"
)

// Format is an Office file format.
type Format string

const (
	Docx Format = "docx"
	Xlsx Format = "xlsx"
	Pptx Format = "pptx"
)

// Templater renders templates with one configuration. Create it once and reuse it; it is safe
// for concurrent use.
type Templater struct {
	opts    Options
	wire    json.RawMessage
	host    hostHandler
	loc     *time.Location
	engine  *Engine
	initErr error
}

// New returns a Templater with the given options.
func New(opts Options) *Templater {
	t := &Templater{opts: opts, wire: opts.wire(), loc: opts.TimeZone}
	t.opts.Filters = make(map[string]Filter, len(opts.Filters))
	for name, f := range opts.Filters {
		t.opts.Filters[name] = f
	}
	t.host = newHost(&t.opts)
	t.engine, t.initErr = engineOrDefault(opts.Engine)
	return t
}

// Render compiles template, renders it with data and returns the document. To render a template
// many times, Compile it once instead.
func (t *Templater) Render(ctx context.Context, template []byte, data any) ([]byte, error) {
	if t.initErr != nil {
		return nil, t.initErr
	}
	enc := encoder{blobBase: 1, loc: t.loc}
	if err := enc.encode(data); err != nil {
		return nil, err
	}
	var req bytes.Buffer
	req.WriteString(`{"op":"renderOnce","template":{"$blob":0},"options":`)
	req.Write(t.wire)
	req.WriteString(`,"data":`)
	req.Write(enc.buf.Bytes())
	req.WriteByte('}')
	_, blobs, err := t.engine.request(ctx, req.Bytes(), append([][]byte{template}, enc.blobs...), t.host)
	if err != nil {
		return nil, err
	}
	return document(blobs)
}

// Inspect describes the tags of a template.
func (t *Templater) Inspect(ctx context.Context, template []byte) (*InspectResult, error) {
	tpl, err := t.Compile(ctx, template)
	if err != nil {
		return nil, err
	}
	defer tpl.Close()
	return tpl.Inspect(ctx)
}

// Compile parses a template once, so that it can be rendered many times. It reports every
// template mistake at once (see Error.Problems). Close the template when done.
func (t *Templater) Compile(ctx context.Context, template []byte) (*Template, error) {
	if t.initErr != nil {
		return nil, t.initErr
	}
	tpl := &Template{
		t:       t,
		source:  slices.Clone(template),
		handles: map[*instance]uint32{},
	}
	err := t.engine.with(ctx, func(inst *instance) error {
		res, err := tpl.compileOn(ctx, inst)
		if err == nil {
			tpl.format, tpl.parts = res.Format, res.Parts
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	runtime.SetFinalizer(tpl, (*Template).Close)
	return tpl, nil
}

// Template is a compiled template. It is safe for concurrent use: each engine instance compiles
// the template on first use and keeps it until Close.
type Template struct {
	t      *Templater
	source []byte
	format Format
	parts  []string

	mu        sync.Mutex
	handles   map[*instance]uint32
	closed    bool
	inspected json.RawMessage
}

type compileResult struct {
	Template uint32   `json:"template"`
	Format   Format   `json:"format"`
	Parts    []string `json:"parts"`
}

var errClosed = errors.New("coffee: the template is closed")

// Format of the template.
func (tpl *Template) Format() Format { return tpl.format }

// Parts are the package parts that contain template text, e.g. "word/document.xml".
func (tpl *Template) Parts() []string { return slices.Clone(tpl.parts) }

func (tpl *Template) compileOn(ctx context.Context, inst *instance) (*compileResult, error) {
	var req bytes.Buffer
	req.WriteString(`{"op":"compile","template":{"$blob":0},"options":`)
	req.Write(tpl.t.wire)
	req.WriteByte('}')
	raw, _, err := inst.request(ctx, req.Bytes(), [][]byte{tpl.source}, nil)
	if err != nil {
		return nil, err
	}
	var res compileResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("coffee: malformed compile result: %w", err)
	}
	tpl.mu.Lock()
	defer tpl.mu.Unlock()
	if tpl.closed {
		inst.queueDispose(res.Template)
		return nil, errClosed
	}
	tpl.handles[inst] = res.Template
	return &res, nil
}

// handle returns the handle of the template on inst, compiling it there if needed.
func (tpl *Template) handle(ctx context.Context, inst *instance) (uint32, error) {
	tpl.mu.Lock()
	if tpl.closed {
		tpl.mu.Unlock()
		return 0, errClosed
	}
	h, ok := tpl.handles[inst]
	tpl.mu.Unlock()
	if ok {
		return h, nil
	}
	res, err := tpl.compileOn(ctx, inst)
	if err != nil {
		return 0, err
	}
	return res.Template, nil
}

// Render renders the template with data and returns the document.
//
// Data is converted like encoding/json does (struct tags included), plus: time.Time → date,
// Date → naive date, Decimal and *big.Float → exact decimal, []byte → binary data (images), Image
// for image tags. Cancelling ctx interrupts the engine.
func (tpl *Template) Render(ctx context.Context, data any) ([]byte, error) {
	enc := encoder{loc: tpl.t.loc}
	if err := enc.encode(data); err != nil {
		return nil, err
	}
	var doc []byte
	err := tpl.t.engine.with(ctx, func(inst *instance) error {
		h, err := tpl.handle(ctx, inst)
		if err != nil {
			return err
		}
		var req bytes.Buffer
		fmt.Fprintf(&req, `{"op":"render","template":%d,"data":`, h)
		req.Write(enc.buf.Bytes())
		req.WriteByte('}')
		_, blobs, err := inst.request(ctx, req.Bytes(), enc.blobs, tpl.t.host)
		if err != nil {
			return err
		}
		doc, err = document(blobs)
		return err
	})
	return doc, err
}

// Inspect describes the tags of the template: the tag tree, the variable paths and a sample data
// shape.
func (tpl *Template) Inspect(ctx context.Context) (*InspectResult, error) {
	tpl.mu.Lock()
	raw := tpl.inspected
	tpl.mu.Unlock()
	if raw == nil {
		err := tpl.t.engine.with(ctx, func(inst *instance) error {
			h, err := tpl.handle(ctx, inst)
			if err != nil {
				return err
			}
			raw, _, err = inst.request(ctx, fmt.Appendf(nil, `{"op":"inspect","template":%d}`, h), nil, nil)
			return err
		})
		if err != nil {
			return nil, err
		}
		tpl.mu.Lock()
		tpl.inspected = raw
		tpl.mu.Unlock()
	}
	var res InspectResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("coffee: malformed inspect result: %w", err)
	}
	return &res, nil
}

// Close frees the template in the engine. Calling it again does nothing.
func (tpl *Template) Close() error {
	tpl.mu.Lock()
	defer tpl.mu.Unlock()
	if tpl.closed {
		return nil
	}
	tpl.closed = true
	for inst, h := range tpl.handles {
		inst.queueDispose(h)
	}
	tpl.handles = nil
	runtime.SetFinalizer(tpl, nil)
	return nil
}

func document(blobs [][]byte) ([]byte, error) {
	if len(blobs) == 0 {
		return nil, errorf(ErrInternal, "The engine returned no document")
	}
	return blobs[0], nil
}

// ------------------------------------------------------------------------------- callbacks

type callRequest struct {
	Call  string            `json:"call"`
	Name  string            `json:"name"`
	Value json.RawMessage   `json:"value"`
	Args  []json.RawMessage `json:"args"`
	Tag   TagInfo           `json:"tag"`
}

func newHost(o *Options) hostHandler {
	if len(o.Filters) == 0 && o.NullValue.Func == nil && o.Stringify == nil {
		return nil
	}
	loc := o.TimeZone
	ok := func(v any) (json.RawMessage, [][]byte) {
		enc := encoder{loc: loc}
		if err := enc.encode(v); err != nil {
			return hostFailure(err.Error()), nil
		}
		out := append([]byte(`{"ok":true,"value":`), enc.buf.Bytes()...)
		return append(out, '}'), enc.blobs
	}
	return func(raw json.RawMessage, blobs [][]byte) (json.RawMessage, [][]byte) {
		var req callRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return hostFailure("malformed callback: " + err.Error()), nil
		}
		switch req.Call {
		case "filter":
			f := o.Filters[req.Name]
			if f == nil {
				return hostFailure(fmt.Sprintf("Unknown filter %q", req.Name)), nil
			}
			value, err := decodeValue(req.Value, blobs)
			if err != nil {
				return hostFailure(err.Error()), nil
			}
			args := make([]any, len(req.Args))
			for i, a := range req.Args {
				if args[i], err = decodeValue(a, blobs); err != nil {
					return hostFailure(err.Error()), nil
				}
			}
			res, err := f(value, args...)
			if err != nil {
				return hostFailure(err.Error()), nil
			}
			return ok(res)
		case "nullValue":
			if o.NullValue.Func == nil {
				return hostFailure("NullValue.Func is not set"), nil
			}
			res, err := o.NullValue.Func(req.Tag)
			if err != nil {
				return hostFailure(err.Error()), nil
			}
			return ok(res)
		case "stringify":
			if o.Stringify == nil {
				return hostFailure("Stringify is not set"), nil
			}
			value, err := decodeValue(req.Value, blobs)
			if err != nil {
				return hostFailure(err.Error()), nil
			}
			s, err := o.Stringify(value)
			if err != nil {
				return hostFailure(err.Error()), nil
			}
			return ok(s)
		}
		return hostFailure(fmt.Sprintf("Unsupported callback %q", req.Call)), nil
	}
}
