package coffee

import (
	"bytes"
	"encoding"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Values travel to the engine as JSON plus tagged values (DOCS.md → Data values):
//
//	time.Time            → {"$date": "2024-05-17T10:00:00.000Z"}
//	Date                 → {"$date": "<as written>"}            (naive dates, as in Excel)
//	Decimal, *big.Float  → {"$decimal": "1234.50"}
//	[]byte               → {"$blob": n}                          (sent as a binary blob, not base64)
//	*big.Int, json.Number→ a number lexeme, without precision loss
//	{"$date": …} in data → {"$literal": …}                      (so it is not mistaken for a tag)

// Decimal is an exact decimal number, e.g. Decimal("1234.50"). The number, fixed, round and
// currency filters round it exactly. Use it for money: coffee.Decimal(d.String()) for
// shopspring/decimal and similar types.
type Decimal string

// Date is a date or date-time in ISO 8601 form, passed to the engine as written. Without a time
// zone it is naive (as in Excel) and prints as written: Date("2024-05-17"),
// Date("2024-05-17T10:00:00"). Values of time.Time are absolute instants instead; see
// Options.TimeZone.
//
// Filters and callbacks receive dates as Date.
type Date string

// DateOf returns the calendar date of t (in t's location) as a naive Date.
func DateOf(t time.Time) Date { return Date(t.Format("2006-01-02")) }

// DateTimeOf returns the wall-clock time of t (in t's location) as a naive Date.
func DateTimeOf(t time.Time) Date { return Date(t.Format("2006-01-02T15:04:05.000")) }

var dateLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04",
	"2006-01-02",
}

// Time parses the date. Naive dates are returned in time.UTC.
func (d Date) Time() (time.Time, error) {
	for _, layout := range dateLayouts {
		if t, err := time.Parse(layout, string(d)); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("coffee: invalid date %q", string(d))
}

// Image is the value of an image tag ({{%logo}}).
type Image struct {
	// Data is the image file: PNG, JPEG, GIF, BMP or SVG are detected; other formats need Extension.
	Data []byte `json:"data"`
	// Extension such as "png", "jpg", "webp", "emf". Detected from Data when empty.
	Extension string `json:"extension,omitempty"`
	// Width and Height in pixels at 96 dpi. A missing one is derived from the aspect ratio; when
	// both are missing, the natural size is used.
	Width  float64 `json:"width,omitempty"`
	Height float64 `json:"height,omitempty"`
	// Alt is the alternative text.
	Alt string `json:"alt,omitempty"`
}

var tagKeys = map[string]bool{"$date": true, "$decimal": true, "$blob": true, "$base64": true, "$literal": true}

// ------------------------------------------------------------------------------- Go → engine

// maxDepth guards against cycles. The engine accepts 128 levels of JSON nesting.
const maxDepth = 200

type encoder struct {
	buf      bytes.Buffer
	blobs    [][]byte
	blobBase int            // index of the first blob this encoder adds (renderOnce: 1)
	loc      *time.Location // Options.TimeZone
}

func (e *encoder) encode(v any) error {
	return e.value(reflect.ValueOf(v), 0)
}

var (
	timeType      = reflect.TypeFor[time.Time]()
	numberType    = reflect.TypeFor[json.Number]()
	rawType       = reflect.TypeFor[json.RawMessage]()
	bigIntType    = reflect.TypeFor[big.Int]()
	bigFloatType  = reflect.TypeFor[big.Float]()
	bigRatType    = reflect.TypeFor[big.Rat]()
	marshalerType = reflect.TypeFor[json.Marshaler]()
	textType      = reflect.TypeFor[encoding.TextMarshaler]()
)

func (e *encoder) value(v reflect.Value, depth int) error {
	if depth > maxDepth {
		return errors.New("coffee: data nests too deep or contains a cycle")
	}
	if !v.IsValid() {
		e.buf.WriteString("null")
		return nil
	}
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			e.buf.WriteString("null")
			return nil
		}
		v = v.Elem()
	}

	t := v.Type()
	switch t {
	case timeType:
		return e.time(v.Interface().(time.Time))
	case numberType:
		return e.number(v.String())
	case rawType:
		return e.raw(v.Bytes())
	case bigIntType:
		b := addr(v).Interface().(*big.Int)
		e.buf.WriteString(b.String())
		return nil
	case bigFloatType:
		f := addr(v).Interface().(*big.Float)
		if f.IsInf() {
			e.buf.WriteString("null")
			return nil
		}
		e.tagged("$decimal", f.Text('f', -1))
		return nil
	case bigRatType:
		r := addr(v).Interface().(*big.Rat)
		if !r.IsInt() {
			if s, exact := r.FloatPrec(); exact {
				e.tagged("$decimal", r.FloatString(s))
				return nil
			}
			f, _ := r.Float64()
			return e.float(f, 64)
		}
		e.buf.WriteString(r.Num().String())
		return nil
	}
	if t.PkgPath() == pkgPath {
		switch x := v.Interface().(type) {
		case Decimal:
			e.tagged("$decimal", string(x))
			return nil
		case Date:
			e.tagged("$date", string(x))
			return nil
		}
	}

	if t.Implements(marshalerType) || (v.CanAddr() && reflect.PointerTo(t).Implements(marshalerType)) {
		m := v.Interface()
		if v.CanAddr() && !t.Implements(marshalerType) {
			m = v.Addr().Interface()
		}
		b, err := m.(json.Marshaler).MarshalJSON()
		if err != nil {
			return fmt.Errorf("coffee: %s.MarshalJSON: %w", t, err)
		}
		return e.raw(b)
	}
	if t.Implements(textType) || (v.CanAddr() && reflect.PointerTo(t).Implements(textType)) {
		m := v.Interface()
		if v.CanAddr() && !t.Implements(textType) {
			m = v.Addr().Interface()
		}
		b, err := m.(encoding.TextMarshaler).MarshalText()
		if err != nil {
			return fmt.Errorf("coffee: %s.MarshalText: %w", t, err)
		}
		e.string(string(b))
		return nil
	}

	switch v.Kind() {
	case reflect.Bool:
		if v.Bool() {
			e.buf.WriteString("true")
		} else {
			e.buf.WriteString("false")
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		e.buf.WriteString(strconv.FormatInt(v.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		e.buf.WriteString(strconv.FormatUint(v.Uint(), 10))
	case reflect.Float32:
		return e.float(v.Float(), 32)
	case reflect.Float64:
		return e.float(v.Float(), 64)
	case reflect.String:
		e.string(v.String())
	case reflect.Slice:
		if v.IsNil() {
			e.buf.WriteString("null")
			return nil
		}
		if t.Elem().Kind() == reflect.Uint8 && !reflect.PointerTo(t.Elem()).Implements(marshalerType) &&
			!reflect.PointerTo(t.Elem()).Implements(textType) {
			e.blob(v.Bytes())
			return nil
		}
		return e.array(v, depth)
	case reflect.Array:
		return e.array(v, depth)
	case reflect.Map:
		if v.IsNil() {
			e.buf.WriteString("null")
			return nil
		}
		return e.mapValue(v, depth)
	case reflect.Struct:
		return e.structValue(v, depth)
	default:
		// functions, channels, complex numbers: like JSON.stringify, there is no value
		e.buf.WriteString("null")
	}
	return nil
}

var pkgPath = reflect.TypeFor[Decimal]().PkgPath()

func special(t reflect.Type) bool {
	return t == timeType || t == bigIntType || t == bigFloatType || t == bigRatType
}

// addr returns a pointer to v, copying v when it is not addressable.
func addr(v reflect.Value) reflect.Value {
	if v.CanAddr() {
		return v.Addr()
	}
	p := reflect.New(v.Type())
	p.Elem().Set(v)
	return p
}

func (e *encoder) time(t time.Time) error {
	if e.loc != nil {
		e.tagged("$date", t.In(e.loc).Format("2006-01-02T15:04:05.000"))
	} else {
		e.tagged("$date", t.UTC().Format("2006-01-02T15:04:05.000Z"))
	}
	return nil
}

func (e *encoder) tagged(tag, s string) {
	e.buf.WriteString(`{"`)
	e.buf.WriteString(tag)
	e.buf.WriteString(`":`)
	e.string(s)
	e.buf.WriteByte('}')
}

func (e *encoder) blob(b []byte) {
	e.blobs = append(e.blobs, b)
	e.buf.WriteString(`{"$blob":`)
	e.buf.WriteString(strconv.Itoa(e.blobBase + len(e.blobs) - 1))
	e.buf.WriteByte('}')
}

func (e *encoder) number(s string) error {
	if s == "" {
		s = "0"
	}
	if !json.Valid([]byte(s)) || strings.ContainsAny(s, `"[{tfn`) {
		return fmt.Errorf("coffee: invalid number %q", s)
	}
	e.buf.WriteString(s)
	return nil
}

func (e *encoder) raw(b []byte) error {
	b = bytes.TrimSpace(b)
	if !json.Valid(b) {
		return fmt.Errorf("coffee: invalid JSON in data: %.40q", b)
	}
	e.buf.Write(b)
	return nil
}

// float formats like encoding/json (and JavaScript): 10.5, 1e+21, 1e-7. NaN and ±Inf become null.
func (e *encoder) float(f float64, bits int) error {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		e.buf.WriteString("null")
		return nil
	}
	e.buf.Write(appendFloat(nil, f, bits))
	return nil
}

func appendFloat(b []byte, f float64, bits int) []byte {
	abs := math.Abs(f)
	format := byte('f')
	if abs != 0 {
		if bits == 64 && (abs < 1e-6 || abs >= 1e21) || bits == 32 && (float32(abs) < 1e-6 || float32(abs) >= 1e21) {
			format = 'e'
		}
	}
	b = strconv.AppendFloat(b, f, format, -1, bits)
	if format == 'e' {
		// clean up e-09 to e-9
		n := len(b)
		if n >= 4 && b[n-4] == 'e' && b[n-3] == '-' && b[n-2] == '0' {
			b[n-2] = b[n-1]
			b = b[:n-1]
		}
	}
	return b
}

func (e *encoder) string(s string) {
	e.buf.WriteByte('"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if c >= 0x20 && c != '"' && c != '\\' {
				i++
				continue
			}
			e.buf.WriteString(s[start:i])
			switch c {
			case '"', '\\':
				e.buf.WriteByte('\\')
				e.buf.WriteByte(c)
			case '\n':
				e.buf.WriteString(`\n`)
			case '\r':
				e.buf.WriteString(`\r`)
			case '\t':
				e.buf.WriteString(`\t`)
			default:
				fmt.Fprintf(&e.buf, `\u%04x`, c)
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			e.buf.WriteString(s[start:i])
			e.buf.WriteString(`�`)
			i += size
			start = i
			continue
		}
		i += size
	}
	e.buf.WriteString(s[start:])
	e.buf.WriteByte('"')
}

func (e *encoder) array(v reflect.Value, depth int) error {
	e.buf.WriteByte('[')
	for i := 0; i < v.Len(); i++ {
		if i > 0 {
			e.buf.WriteByte(',')
		}
		if err := e.value(v.Index(i), depth+1); err != nil {
			return err
		}
	}
	e.buf.WriteByte(']')
	return nil
}

// object writes the members produced by each and wraps a tag-looking object in $literal.
func (e *encoder) object(depth int, each func(member func(key string, v reflect.Value) error) error) error {
	start := e.buf.Len()
	e.buf.WriteByte('{')
	n := 0
	first := ""
	err := each(func(key string, v reflect.Value) error {
		if n > 0 {
			e.buf.WriteByte(',')
		} else {
			first = key
		}
		n++
		e.string(key)
		e.buf.WriteByte(':')
		return e.value(v, depth+1)
	})
	if err != nil {
		return err
	}
	e.buf.WriteByte('}')
	if n == 1 && tagKeys[first] {
		obj := slices.Clone(e.buf.Bytes()[start:])
		e.buf.Truncate(start)
		e.buf.WriteString(`{"$literal":`)
		e.buf.Write(obj)
		e.buf.WriteByte('}')
	}
	return nil
}

func (e *encoder) mapValue(v reflect.Value, depth int) error {
	type kv struct {
		key string
		val reflect.Value
	}
	members := make([]kv, 0, v.Len())
	it := v.MapRange()
	for it.Next() {
		k, err := mapKey(it.Key())
		if err != nil {
			return err
		}
		members = append(members, kv{k, it.Value()})
	}
	slices.SortFunc(members, func(a, b kv) int { return strings.Compare(a.key, b.key) })
	return e.object(depth, func(member func(string, reflect.Value) error) error {
		for _, m := range members {
			if err := member(m.key, m.val); err != nil {
				return err
			}
		}
		return nil
	})
}

func mapKey(k reflect.Value) (string, error) {
	if k.Kind() == reflect.String {
		return k.String(), nil
	}
	if tm, ok := k.Interface().(encoding.TextMarshaler); ok {
		if k.Kind() == reflect.Pointer && k.IsNil() {
			return "", nil
		}
		b, err := tm.MarshalText()
		return string(b), err
	}
	switch k.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(k.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(k.Uint(), 10), nil
	}
	return "", fmt.Errorf("coffee: unsupported map key type %s", k.Type())
}

func (e *encoder) structValue(v reflect.Value, depth int) error {
	fields := cachedFields(v.Type())
	return e.object(depth, func(member func(string, reflect.Value) error) error {
	next:
		for _, f := range fields {
			fv := v
			for _, i := range f.index {
				if fv.Kind() == reflect.Pointer {
					if fv.IsNil() {
						continue next
					}
					fv = fv.Elem()
				}
				fv = fv.Field(i)
			}
			if f.omitEmpty && isEmpty(fv) || f.omitZero && fv.IsZero() {
				continue
			}
			if err := member(f.name, fv); err != nil {
				return err
			}
		}
		return nil
	})
}

func isEmpty(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Interface, reflect.Pointer:
		return v.IsZero()
	}
	return false
}

// ------------------------------------------------------------------------------- struct fields

type field struct {
	name      string
	index     []int
	tagged    bool
	omitEmpty bool
	omitZero  bool
}

var fieldCache sync.Map // reflect.Type → []field

func cachedFields(t reflect.Type) []field {
	if f, ok := fieldCache.Load(t); ok {
		return f.([]field)
	}
	f, _ := fieldCache.LoadOrStore(t, typeFields(t))
	return f.([]field)
}

// typeFields follows the rules of encoding/json: `json` tags, "-", omitempty, omitzero, and
// promoted fields of embedded structs, where the shallowest (then tagged) field wins.
func typeFields(t reflect.Type) []field {
	type queued struct {
		typ   reflect.Type
		index []int
	}
	var fields []field
	current := []queued{}
	next := []queued{{typ: t}}
	visited := map[reflect.Type]bool{}

	for len(next) > 0 {
		current, next = next, current[:0]
		var level []field
		for _, q := range current {
			if visited[q.typ] {
				continue
			}
			visited[q.typ] = true
			for i := 0; i < q.typ.NumField(); i++ {
				sf := q.typ.Field(i)
				if sf.Anonymous {
					ft := sf.Type
					if ft.Kind() == reflect.Pointer {
						ft = ft.Elem()
					}
					if !sf.IsExported() && ft.Kind() != reflect.Struct {
						continue
					}
				} else if !sf.IsExported() {
					continue
				}
				tag := sf.Tag.Get("json")
				if tag == "-" {
					continue
				}
				name, opts, _ := strings.Cut(tag, ",")
				index := append(slices.Clone(q.index), i)
				ft := sf.Type
				if ft.Name() == "" && ft.Kind() == reflect.Pointer {
					ft = ft.Elem()
				}
				if name == "" && sf.Anonymous && ft.Kind() == reflect.Struct && !special(ft) {
					next = append(next, queued{typ: ft, index: index})
					continue
				}
				f := field{name: name, index: index, tagged: name != ""}
				if f.name == "" {
					f.name = sf.Name
				}
				for _, o := range strings.Split(opts, ",") {
					switch o {
					case "omitempty":
						f.omitEmpty = true
					case "omitzero":
						f.omitZero = true
					}
				}
				level = append(level, f)
			}
		}
		// fields of this depth hide deeper ones with the same name; ambiguous untagged duplicates vanish
		byName := map[string][]field{}
		for _, f := range level {
			byName[f.name] = append(byName[f.name], f)
		}
		taken := map[string]bool{}
		for _, f := range fields {
			taken[f.name] = true
		}
		for _, f := range level {
			if taken[f.name] {
				continue
			}
			dups := byName[f.name]
			if len(dups) > 1 {
				var tagged []field
				for _, d := range dups {
					if d.tagged {
						tagged = append(tagged, d)
					}
				}
				if len(tagged) != 1 {
					taken[f.name] = true
					continue
				}
				f = tagged[0]
			}
			taken[f.name] = true
			fields = append(fields, f)
		}
	}
	slices.SortStableFunc(fields, func(a, b field) int { return slices.Compare(a.index, b.index) })
	return fields
}

// ------------------------------------------------------------------------------- engine → Go

// decodeValue converts engine JSON into Go values: objects → map[string]any, arrays → []any,
// numbers → float64, $date → Date, $decimal → Decimal, $blob/$base64 → []byte, $literal → its
// content as is.
func decodeValue(raw json.RawMessage, blobs [][]byte) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	return untag(v, blobs, false)
}

func untag(v any, blobs [][]byte, literal bool) (any, error) {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return nil, err
		}
		return f, nil
	case []any:
		for i := range x {
			var err error
			if x[i], err = untag(x[i], blobs, literal); err != nil {
				return nil, err
			}
		}
		return x, nil
	case map[string]any:
		if !literal && len(x) == 1 {
			for k, val := range x {
				if !tagKeys[k] {
					break
				}
				s, _ := val.(string)
				switch k {
				case "$date":
					return Date(s), nil
				case "$decimal":
					return Decimal(s), nil
				case "$base64":
					return decodeBase64(s)
				case "$blob":
					n, ok := val.(json.Number)
					i, err := n.Int64()
					if !ok || err != nil || i < 0 || i >= int64(len(blobs)) {
						return nil, fmt.Errorf("coffee: invalid blob reference %v", val)
					}
					return blobs[i], nil
				case "$literal":
					return untag(val, blobs, true)
				}
			}
		}
		for k, val := range x {
			var err error
			if x[k], err = untag(val, blobs, literal); err != nil {
				return nil, err
			}
		}
		return x, nil
	}
	return v, nil
}

func decodeBase64(s string) ([]byte, error) {
	if strings.HasPrefix(s, "data:") {
		if i := strings.Index(s, ";base64,"); i >= 0 {
			s = s[i+len(";base64,"):]
		}
	}
	s = strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, s)
	s = strings.TrimRight(s, "=")
	return base64.RawStdEncoding.DecodeString(s)
}
