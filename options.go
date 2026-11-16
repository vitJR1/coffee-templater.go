package coffee

import (
	"encoding/json"
	"slices"
	"time"
)

// Options configure a Templater. The zero value gives the defaults.
type Options struct {
	// Delimiters of tags. Default {{ and }}. For example Delimiters{"${", "}"} for ${name}.
	Delimiters Delimiters
	// EscapeChar escapes a delimiter so that it is printed literally: \{{not a tag}}. Default `\`.
	EscapeChar string
	// NoEscape disables escaping.
	NoEscape bool
	// Syntax overrides sigils and keywords of the tag syntax. Empty fields keep the default.
	Syntax Syntax

	// Filters are custom filters: {{ price | money: "EUR" }}. They override built-in filters with the
	// same name.
	Filters map[string]Filter
	// Strict reports missing values as ErrMissingValue instead of printing them as empty.
	Strict bool
	// NullValue decides what a tag resolving to null or a missing value prints. Ignored in Strict mode.
	NullValue NullValue
	// Stringify, when set, converts every non-string value to text (numbers, booleans, dates,
	// arrays, objects). By default numbers print as written, dates as ISO, arrays joined with ", "
	// and objects as JSON.
	Stringify func(value any) (string, error)
	// DateFormat is the default format of dates printed without the date filter, e.g. "DD.MM.YYYY".
	// Default: ISO.
	DateFormat string
	// TimeZone, when set, sends time.Time values as the naive wall-clock time in this location, so
	// {{ d | date: "DD.MM.YYYY" }} shows the local day. By default they are absolute instants
	// printed in UTC. Use Date for values that are naive already.
	TimeZone *time.Location
	// NoLinebreaks keeps "\n" in values as is instead of turning it into a line break
	// (and "\t" into a tab in Word).
	NoLinebreaks bool

	// Limits protect against hostile templates and data.
	Limits Limits
	// Engine runs the templates. Default: DefaultEngine().
	Engine *Engine
}

// Delimiters of tags.
type Delimiters struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// Syntax holds the sigils and keywords of tags. Empty fields keep the default.
type Syntax struct {
	Section  string `json:"section,omitempty"`  // "#": {{#items}}
	Inverted string `json:"inverted,omitempty"` // "^": {{^items}}
	Close    string `json:"close,omitempty"`    // "/": {{/items}}
	Image    string `json:"image,omitempty"`    // "%": {{%logo}}
	Else     string `json:"else,omitempty"`     // "else": {{else}}
	If       string `json:"if,omitempty"`       // "if": {{#if paid}}
	Unless   string `json:"unless,omitempty"`   // "unless": {{#unless paid}}
	Each     string `json:"each,omitempty"`     // "each": {{#each items}}
}

// Filter implements a custom filter. It receives the piped value and the arguments,
// {{ value | name: arg1, arg2 }}, converted as described for Filter values: objects are
// map[string]any, arrays []any, numbers float64, dates Date, decimals Decimal, binary data []byte.
// It may return any value that could appear in template data. An error fails the render with
// ErrFilterFailed.
type Filter func(value any, args ...any) (any, error)

// TagInfo describes a tag, for NullValue.Func.
type TagInfo struct {
	// Raw is the full tag text with delimiters, e.g. "{{client.name}}".
	Raw string `json:"raw"`
	// Expression is the text inside the tag, e.g. "client.name | upper".
	Expression string `json:"expression"`
	// Part is the package part of the tag, e.g. "word/document.xml".
	Part string `json:"part"`
}

// NullMode selects how null and missing values print.
type NullMode string

const (
	NullEmpty NullMode = "empty" // print nothing (default)
	NullText  NullMode = "text"  // print NullValue.Text
	NullKeep  NullMode = "keep"  // print the original tag
	NullError NullMode = "error" // fail with ErrMissingValue, like Strict
)

// NullValue configures null and missing values. Func takes precedence over Mode.
type NullValue struct {
	Mode NullMode
	Text string
	// Func returns the value to print for a tag that resolved to null or a missing value.
	Func func(tag TagInfo) (any, error)
}

// Unlimited disables a limit.
const Unlimited = -1

// Limits protect against hostile input. Zero keeps the engine default; Unlimited (-1) disables a
// limit.
type Limits struct {
	MaxInputBytes        int64 // input file size; default 100 MiB
	MaxEntries           int64 // zip entries; default 10 000
	MaxUncompressedBytes int64 // total uncompressed size; default 500 MiB
	MaxCompressionRatio  int64 // uncompressed / compressed, per entry larger than 1 MiB; default 200
	MaxXMLDepth          int64 // XML nesting depth; default 256
	MaxOutputBytes       int64 // result size before compression; default 500 MiB
	MaxLoopIterations    int64 // total loop iterations per render; default 1 000 000
	MaxHostCalls         int64 // calls to Filters, NullValue.Func and Stringify per render; default 1 000 000
}

func (l Limits) wire() map[string]int64 {
	out := map[string]int64{}
	set := func(name string, v int64) {
		switch {
		case v < 0:
			out[name] = 0
		case v > 0:
			out[name] = v
		}
	}
	set("maxInputBytes", l.MaxInputBytes)
	set("maxEntries", l.MaxEntries)
	set("maxUncompressedBytes", l.MaxUncompressedBytes)
	set("maxCompressionRatio", l.MaxCompressionRatio)
	set("maxXmlDepth", l.MaxXMLDepth)
	set("maxOutputBytes", l.MaxOutputBytes)
	set("maxLoopIterations", l.MaxLoopIterations)
	set("maxHostCalls", l.MaxHostCalls)
	if len(out) == 0 {
		return nil
	}
	return out
}

// wire converts Options to the engine options (DOCS.md → Options).
func (o *Options) wire() json.RawMessage {
	type nullValue struct {
		Mode string `json:"mode"`
		Text string `json:"text,omitempty"`
	}
	type stringify struct {
		Mode       string `json:"mode,omitempty"`
		DateFormat string `json:"dateFormat,omitempty"`
	}
	w := struct {
		Delimiters  *Delimiters      `json:"delimiters,omitempty"`
		EscapeChar  any              `json:"escapeChar,omitempty"`
		Syntax      *Syntax          `json:"syntax,omitempty"`
		Strict      bool             `json:"strict,omitempty"`
		Linebreaks  *bool            `json:"linebreaks,omitempty"`
		HostFilters []string         `json:"hostFilters,omitempty"`
		NullValue   *nullValue       `json:"nullValue,omitempty"`
		Stringify   *stringify       `json:"stringify,omitempty"`
		Limits      map[string]int64 `json:"limits,omitempty"`
	}{Strict: o.Strict, Limits: o.Limits.wire()}

	if o.Delimiters != (Delimiters{}) {
		d := o.Delimiters
		w.Delimiters = &d
	}
	switch {
	case o.NoEscape:
		w.EscapeChar = json.RawMessage("null")
	case o.EscapeChar != "":
		w.EscapeChar = o.EscapeChar
	}
	if o.Syntax != (Syntax{}) {
		s := o.Syntax
		w.Syntax = &s
	}
	if o.NoLinebreaks {
		f := false
		w.Linebreaks = &f
	}
	for name := range o.Filters {
		w.HostFilters = append(w.HostFilters, name)
	}
	slices.Sort(w.HostFilters)
	switch {
	case o.NullValue.Func != nil:
		w.NullValue = &nullValue{Mode: "host"}
	case o.NullValue.Mode != "":
		w.NullValue = &nullValue{Mode: string(o.NullValue.Mode), Text: o.NullValue.Text}
	}
	if o.Stringify != nil || o.DateFormat != "" {
		w.Stringify = &stringify{DateFormat: o.DateFormat}
		if o.Stringify != nil {
			w.Stringify.Mode = "host"
		}
	}
	b, _ := json.Marshal(w)
	return b
}
