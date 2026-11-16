package coffee

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

// The document model is a plain representation of an Office file. Parse a file, edit the model and
// Build it back together with the original: properties the model does not cover are kept as raw
// XML (Props, XML fields), and fields are written back only when they change, so a round trip does
// not lose formatting. See DOCS.md → Document model of the engine.

// ModelVersion is the version of the document model of the embedded engine.
const ModelVersion = 1

// Document is a parsed Office file. Depending on Format, Parts (docx), Sheets (xlsx) or Slides
// (pptx) is set.
type Document struct {
	Format       Format `json:"format"`
	ModelVersion int    `json:"modelVersion,omitempty"`

	Parts  []DocxPart  `json:"parts,omitempty"`
	Sheets []XlsxSheet `json:"sheets,omitempty"`
	Slides []PptxSlide `json:"slides,omitempty"`
}

func (d Document) MarshalJSON() ([]byte, error) {
	type plain Document
	p := plain(d)
	var buf bytes.Buffer
	// the collection of the format is required, even when empty
	switch d.Format {
	case Docx:
		if p.Parts == nil {
			p.Parts = []DocxPart{}
		}
		fmt.Fprintf(&buf, `{"format":"docx"`)
		if p.ModelVersion != 0 {
			fmt.Fprintf(&buf, `,"modelVersion":%d`, p.ModelVersion)
		}
		return appendMember(&buf, "parts", p.Parts)
	case Xlsx:
		if p.Sheets == nil {
			p.Sheets = []XlsxSheet{}
		}
		fmt.Fprintf(&buf, `{"format":"xlsx"`)
		if p.ModelVersion != 0 {
			fmt.Fprintf(&buf, `,"modelVersion":%d`, p.ModelVersion)
		}
		return appendMember(&buf, "sheets", p.Sheets)
	case Pptx:
		if p.Slides == nil {
			p.Slides = []PptxSlide{}
		}
		fmt.Fprintf(&buf, `{"format":"pptx"`)
		if p.ModelVersion != 0 {
			fmt.Fprintf(&buf, `,"modelVersion":%d`, p.ModelVersion)
		}
		return appendMember(&buf, "slides", p.Slides)
	}
	return json.Marshal(p)
}

func appendMember(buf *bytes.Buffer, name string, v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(buf, `,%q:`, name)
	buf.Write(b)
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// ------------------------------------------------------------------------------- Word

// DocxPart is the body, a header or a footer.
type DocxPart struct {
	// Path in the package, e.g. "word/document.xml", "word/header1.xml".
	Path string `json:"path"`
	// Kind is "body", "header" or "footer".
	Kind   string      `json:"kind"`
	Blocks []DocxBlock `json:"blocks"`
	// SectionProps is the final <w:sectPr> of the body (page size, margins, header references).
	SectionProps string `json:"sectionProps,omitempty"`
}

func (p DocxPart) MarshalJSON() ([]byte, error) {
	type plain DocxPart
	if p.Blocks == nil {
		p.Blocks = []DocxBlock{}
	}
	return json.Marshal(plain(p))
}

// DocxBlock is a paragraph, a table or raw XML, by Type.
type DocxBlock struct {
	// Type is "paragraph", "table" or "raw".
	Type string `json:"type"`

	// paragraph
	Style    string       `json:"style,omitempty"` // paragraph style id, e.g. "Heading1"
	Align    string       `json:"align,omitempty"` // left | center | right | justify
	List     *ListRef     `json:"list,omitempty"`  // numbering (bullets, numbered lists)
	Children []DocxInline `json:"children,omitempty"`

	// table
	Rows []DocxTableRow `json:"rows,omitempty"`
	Grid string         `json:"grid,omitempty"` // original <w:tblGrid>

	// Props is the original <w:pPr> (paragraph) or <w:tblPr> (table). Fields above override it.
	Props string `json:"props,omitempty"`
	// XML of a raw block (content controls and anything else not modelled).
	XML string `json:"xml,omitempty"`
}

func (b DocxBlock) MarshalJSON() ([]byte, error) {
	type plain DocxBlock
	if b.Type == "table" && b.Rows == nil {
		b.Rows = []DocxTableRow{}
	}
	if b.Type == "raw" {
		return json.Marshal(struct {
			Type string `json:"type"`
			XML  string `json:"xml"`
		}{b.Type, b.XML})
	}
	return json.Marshal(plain(b))
}

// ListRef is the numbering of a paragraph.
type ListRef struct {
	ID    int `json:"id"`
	Level int `json:"level"`
}

// DocxInline is a run, a break, a hyperlink or raw XML, by Type.
type DocxInline struct {
	// Type is "run", "break", "hyperlink" or "raw".
	Type string `json:"type"`

	// run: Text, where "\n" is a line break and "\t" a tab, and its formatting
	Text string `json:"text,omitempty"`
	DocxRunStyle

	// break: "page" or "column"
	Kind string `json:"kind,omitempty"`

	// hyperlink: an external URL or an internal bookmark, and its content
	URL      string       `json:"url,omitempty"`
	Anchor   string       `json:"anchor,omitempty"`
	Children []DocxInline `json:"children,omitempty"`

	// Props is the original <w:rPr> of a run. Style fields override it.
	Props string `json:"props,omitempty"`
	// XML of raw content (images, fields, bookmarks…).
	XML string `json:"xml,omitempty"`
}

func (in DocxInline) MarshalJSON() ([]byte, error) {
	type plain DocxInline
	if in.Type == "raw" {
		return json.Marshal(struct {
			Type string `json:"type"`
			XML  string `json:"xml"`
		}{in.Type, in.XML})
	}
	return json.Marshal(plain(in))
}

// DocxRunStyle is the formatting of a run. Nil fields are unset.
type DocxRunStyle struct {
	Style     string     `json:"style,omitempty"` // character style id
	Bold      *bool      `json:"bold,omitempty"`
	Italic    *bool      `json:"italic,omitempty"`
	Underline *Underline `json:"underline,omitempty"`
	Strike    *bool      `json:"strike,omitempty"`
	Color     string     `json:"color,omitempty"`     // hex without #, e.g. "FF0000"
	Highlight string     `json:"highlight,omitempty"` // e.g. "yellow"
	Size      *float64   `json:"size,omitempty"`      // points
	Font      string     `json:"font,omitempty"`
	VertAlign string     `json:"vertAlign,omitempty"` // superscript | subscript | baseline
}

// Underline is on/off, or an underline type such as "double" or "wave".
type Underline struct {
	On   bool
	Type string // when set, the underline type; On is implied
}

func (u Underline) MarshalJSON() ([]byte, error) {
	if u.Type != "" {
		return json.Marshal(u.Type)
	}
	return json.Marshal(u.On)
}

func (u *Underline) UnmarshalJSON(b []byte) error {
	*u = Underline{}
	if len(b) > 0 && b[0] == '"' {
		if err := json.Unmarshal(b, &u.Type); err != nil {
			return err
		}
		u.On = u.Type != "none"
		return nil
	}
	return json.Unmarshal(b, &u.On)
}

// DocxTableRow is a table row.
type DocxTableRow struct {
	Cells []DocxTableCell `json:"cells"`
	Props string          `json:"props,omitempty"` // original <w:trPr>
}

func (r DocxTableRow) MarshalJSON() ([]byte, error) {
	type plain DocxTableRow
	if r.Cells == nil {
		r.Cells = []DocxTableCell{}
	}
	return json.Marshal(plain(r))
}

// DocxTableCell is a table cell.
type DocxTableCell struct {
	Blocks  []DocxBlock `json:"blocks,omitempty"`
	ColSpan int         `json:"colSpan,omitempty"`
	VMerge  string      `json:"vMerge,omitempty"` // restart | continue
	Props   string      `json:"props,omitempty"`  // original <w:tcPr>
}

// ------------------------------------------------------------------------------- Excel

// XlsxSheet is a worksheet.
type XlsxSheet struct {
	Name string `json:"name"`
	// Path of an existing sheet. Leave it empty to create a new sheet on Build.
	Path string    `json:"path,omitempty"`
	Rows []XlsxRow `json:"rows,omitempty"`
	// Merges are merged ranges, e.g. "A1:C1".
	Merges []string `json:"merges,omitempty"`
}

// XlsxRow is a sheet row.
type XlsxRow struct {
	// Index is the 1-based row number.
	Index  int        `json:"index"`
	Height *float64   `json:"height,omitempty"` // points
	Hidden *bool      `json:"hidden,omitempty"`
	Cells  []XlsxCell `json:"cells,omitempty"`
}

// XlsxCell is a cell.
type XlsxCell struct {
	// Ref is the A1-style reference. When empty, the cell goes to the column after the previous one.
	Ref string `json:"ref,omitempty"`
	// Value is a string, float64, bool or nil. Date cells hold an ISO string with Type "date".
	Value any `json:"value,omitempty"`
	// Type is "date" for date-formatted numbers, "error" for #N/A and the like.
	Type string `json:"type,omitempty"`
	// Formula without the leading =. Value holds the cached result.
	Formula string `json:"formula,omitempty"`
	// Style is an index into the workbook cell formats (cellXfs).
	Style *int `json:"style,omitempty"`
	// RichText is the original rich-text XML (<r> runs), used while Value equals its plain text.
	RichText string `json:"richText,omitempty"`
}

// ------------------------------------------------------------------------------- PowerPoint

// PptxSlide is a slide.
type PptxSlide struct {
	// Path in the package, e.g. "ppt/slides/slide1.xml".
	Path string `json:"path"`
	// Shapes in z-order. Shapes missing from the list are removed on Build.
	Shapes []PptxShape `json:"shapes,omitempty"`
	// Notes are the speaker notes as plain text.
	Notes string `json:"notes,omitempty"`
}

// PptxShape is a shape of a slide, by Type.
type PptxShape struct {
	// Type is "text", "table", "picture", "group", "connector", "graphic" or "other".
	Type string `json:"type"`
	// ID matches shapes on Build (p:cNvPr/@id).
	ID   int64  `json:"id,omitempty"`
	Name string `json:"name,omitempty"`

	// text: Placeholder is the placeholder type, e.g. "title", "body", "subTitle"
	Placeholder string          `json:"placeholder,omitempty"`
	Paragraphs  []PptxParagraph `json:"paragraphs,omitempty"`

	// table
	Rows []PptxTableRow `json:"rows,omitempty"`

	// XML of other shapes; only needed to insert a new shape.
	XML string `json:"xml,omitempty"`
}

// PptxTableRow is a row of a table shape.
type PptxTableRow struct {
	Cells []PptxTableCell `json:"cells,omitempty"`
}

// PptxTableCell is a cell of a table shape.
type PptxTableCell struct {
	Paragraphs []PptxParagraph `json:"paragraphs,omitempty"`
}

// PptxParagraph is a paragraph of a text shape or a table cell.
type PptxParagraph struct {
	Align    string       `json:"align,omitempty"` // left | center | right | justify
	Level    *int         `json:"level,omitempty"` // outline level, 0-based
	Children []PptxInline `json:"children,omitempty"`
	Props    string       `json:"props,omitempty"`    // original <a:pPr>
	EndProps string       `json:"endProps,omitempty"` // original <a:endParaRPr>
}

// PptxInline is a run or raw XML (fields such as the slide number), by Type.
type PptxInline struct {
	// Type is "run" or "raw".
	Type string `json:"type"`

	// run: Text, where "\n" is a line break, and its formatting
	Text      string     `json:"text,omitempty"`
	Bold      *bool      `json:"bold,omitempty"`
	Italic    *bool      `json:"italic,omitempty"`
	Underline *Underline `json:"underline,omitempty"`
	Strike    *bool      `json:"strike,omitempty"`
	Size      *float64   `json:"size,omitempty"`  // points
	Color     string     `json:"color,omitempty"` // hex RGB without #
	Font      string     `json:"font,omitempty"`
	Props     string     `json:"props,omitempty"` // original <a:rPr>

	// XML of raw content.
	XML string `json:"xml,omitempty"`
}

func (in PptxInline) MarshalJSON() ([]byte, error) {
	type plain PptxInline
	if in.Type == "raw" {
		return json.Marshal(struct {
			Type string `json:"type"`
			XML  string `json:"xml"`
		}{in.Type, in.XML})
	}
	return json.Marshal(plain(in))
}

// ------------------------------------------------------------------------------- operations

// Parse turns an Office file into a Document. Edit it and pass it to Build with the original file.
func Parse(ctx context.Context, doc []byte, limits ...Limits) (*Document, error) {
	e, err := DefaultEngine()
	if err != nil {
		return nil, err
	}
	return e.Parse(ctx, doc, limits...)
}

// Build builds an Office file from a model. Base is the original file: styles, media and everything
// else not in the model come from it. It may be nil for docx and xlsx (a blank document is used)
// and is required for pptx.
func Build(ctx context.Context, model *Document, base []byte) ([]byte, error) {
	e, err := DefaultEngine()
	if err != nil {
		return nil, err
	}
	return e.Build(ctx, model, base)
}

// ExtractText returns the plain text of an Office file: paragraphs (docx), tab-separated cells
// under a sheet name (xlsx), or "--- Slide N ---" blocks with notes (pptx).
func ExtractText(ctx context.Context, doc []byte, limits ...Limits) (string, error) {
	e, err := DefaultEngine()
	if err != nil {
		return "", err
	}
	return e.ExtractText(ctx, doc, limits...)
}

// DetectFormat detects the format of an Office file.
func DetectFormat(ctx context.Context, doc []byte, limits ...Limits) (Format, error) {
	e, err := DefaultEngine()
	if err != nil {
		return "", err
	}
	return e.DetectFormat(ctx, doc, limits...)
}

// Parse is Parse on this engine.
func (e *Engine) Parse(ctx context.Context, doc []byte, limits ...Limits) (*Document, error) {
	raw, err := e.documentOp(ctx, "parse", doc, limits)
	if err != nil {
		return nil, err
	}
	var res struct {
		Model Document `json:"model"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("coffee: malformed model: %w", err)
	}
	return &res.Model, nil
}

// Build is Build on this engine.
func (e *Engine) Build(ctx context.Context, model *Document, base []byte) ([]byte, error) {
	if model == nil {
		return nil, errorf(ErrInvalidModel, "The model is nil")
	}
	m, err := json.Marshal(model)
	if err != nil {
		return nil, fmt.Errorf("coffee: encode model: %w", err)
	}
	var req bytes.Buffer
	req.WriteString(`{"op":"build","model":`)
	req.Write(m)
	var blobs [][]byte
	if base != nil {
		req.WriteString(`,"base":{"$blob":0}`)
		blobs = [][]byte{base}
	}
	req.WriteByte('}')
	_, out, err := e.request(ctx, req.Bytes(), blobs, nil)
	if err != nil {
		return nil, err
	}
	return document(out)
}

// ExtractText is ExtractText on this engine.
func (e *Engine) ExtractText(ctx context.Context, doc []byte, limits ...Limits) (string, error) {
	raw, err := e.documentOp(ctx, "extractText", doc, limits)
	if err != nil {
		return "", err
	}
	var res struct {
		Text string `json:"text"`
	}
	err = json.Unmarshal(raw, &res)
	return res.Text, err
}

// DetectFormat is DetectFormat on this engine.
func (e *Engine) DetectFormat(ctx context.Context, doc []byte, limits ...Limits) (Format, error) {
	raw, err := e.documentOp(ctx, "detect", doc, limits)
	if err != nil {
		return "", err
	}
	var res struct {
		Format Format `json:"format"`
	}
	err = json.Unmarshal(raw, &res)
	return res.Format, err
}

// Info describes the embedded engine.
type Info struct {
	CoreVersion  string         `json:"coreVersion"`
	ABIVersion   int            `json:"abiVersion"`
	ModelVersion int            `json:"modelVersion"`
	Formats      []Format       `json:"formats"`
	Features     map[string]any `json:"features"`
}

// Info returns the versions and capabilities of the engine.
func (e *Engine) Info(ctx context.Context) (*Info, error) {
	raw, _, err := e.request(ctx, []byte(`{"op":"info"}`), nil, nil)
	if err != nil {
		return nil, err
	}
	var info Info
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func (e *Engine) documentOp(ctx context.Context, op string, doc []byte, limits []Limits) (json.RawMessage, error) {
	var req bytes.Buffer
	fmt.Fprintf(&req, `{"op":%q,"document":{"$blob":0}`, op)
	if len(limits) > 0 {
		if l := limits[0].wire(); l != nil {
			b, _ := json.Marshal(l)
			req.WriteString(`,"limits":`)
			req.Write(b)
		}
	}
	req.WriteByte('}')
	raw, _, err := e.request(ctx, req.Bytes(), [][]byte{doc}, nil)
	return raw, err
}
