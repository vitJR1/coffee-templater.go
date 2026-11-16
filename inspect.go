package coffee

// InspectResult describes the tags of a template.
type InspectResult struct {
	Format Format `json:"format"`
	// Parts holds the tag tree of each package part.
	Parts []PartTags `json:"parts"`
	// Tags is the tag tree of the whole document, all parts in order.
	Tags []Tag `json:"tags"`
	// Variables lists every data path used, loops marked with []: "client.name", "items[].price".
	Variables []string `json:"variables"`
	// Schema is a sample data object with the shape the template expects: strings for variables,
	// arrays for loops, booleans for conditions and nil for images.
	Schema map[string]any `json:"schema"`
}

// PartTags is the tag tree of one package part.
type PartTags struct {
	Part string `json:"part"`
	Tags []Tag  `json:"tags"`
}

// TagType is the kind of a tag.
type TagType string

const (
	TagVariable TagType = "variable"
	TagImage    TagType = "image"
	TagSection  TagType = "section"
)

// SectionMode is the kind of a section tag.
type SectionMode string

const (
	ModeSection  SectionMode = "section"  // {{#x}}: loop, context or truthy check
	ModeInverted SectionMode = "inverted" // {{^x}}
	ModeIf       SectionMode = "if"       // {{#if x}}
	ModeUnless   SectionMode = "unless"   // {{#unless x}}
	ModeEach     SectionMode = "each"     // {{#each x}}
)

// Tag is a node of the tag tree.
type Tag struct {
	Type       TagType `json:"type"`
	Path       string  `json:"path"`
	Expression string  `json:"expression"`
	Raw        string  `json:"raw"`
	// Filters of a variable tag.
	Filters []string `json:"filters,omitempty"`
	// Mode, Children and ElseChildren belong to section tags.
	Mode         SectionMode `json:"mode,omitempty"`
	Children     []Tag       `json:"children,omitempty"`
	ElseChildren []Tag       `json:"elseChildren,omitempty"`
}
