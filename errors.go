package coffee

import (
	"fmt"
	"strings"
)

// ErrorCode identifies a problem reported by the engine. Codes are also errors, so they work with
// errors.Is:
//
//	if errors.Is(err, coffee.ErrUnclosedSection) { … }
type ErrorCode string

func (c ErrorCode) Error() string { return string(c) }

// Template mistakes.
const (
	ErrUnclosedTag       ErrorCode = "unclosed_tag"       // missing closing delimiter, or a tag spanning paragraphs/cells
	ErrUnopenedTag       ErrorCode = "unopened_tag"       // closing delimiter without an opening one
	ErrUnclosedSection   ErrorCode = "unclosed_section"   // a section that is never closed
	ErrUnmatchedClose    ErrorCode = "unmatched_close"    // a closing tag without an opening tag, or else outside a section
	ErrInvalidExpression ErrorCode = "invalid_expression" // syntax error inside a tag, or an empty tag
	ErrUnknownFilter     ErrorCode = "unknown_filter"     // a filter that is neither built in nor in Options.Filters
	ErrFilterFailed      ErrorCode = "filter_failed"      // a Go filter returned an error; see Error.Details["hostMessage"]
	ErrMissingValue      ErrorCode = "missing_value"      // strict mode, value missing
	ErrInvalidSection    ErrorCode = "invalid_section"    // sections overlap or cross structure boundaries; misplaced else
	ErrInvalidImage      ErrorCode = "invalid_image"      // the image value is not an image, or the tag is outside a shape (pptx)
	ErrUnsupported       ErrorCode = "unsupported"        // feature not available in this format, malformed XML structure
)

// Engine-level problems.
const (
	ErrInvalidRequest    ErrorCode = "invalid_request"    // bad frame, invalid fields or tagged values
	ErrInvalidOptions    ErrorCode = "invalid_options"    // invalid Options
	ErrInvalidDocument   ErrorCode = "invalid_document"   // not a zip, broken XML, unsafe zip entries
	ErrUnsupportedFormat ErrorCode = "unsupported_format" // not docx/xlsx/pptx
	ErrInvalidModel      ErrorCode = "invalid_model"      // model does not match the schema
	ErrUnknownTemplate   ErrorCode = "unknown_template"   // template handle not found
	ErrLimitExceeded     ErrorCode = "limit_exceeded"     // a limit was exceeded; Details["limit"] names it
	ErrHostError         ErrorCode = "host_error"         // a callback (NullValue, Stringify) failed
	ErrInternal          ErrorCode = "internal"           // a bug in the engine, or the engine crashed
)

// Location tells where inside a part an error happened. Fields are filled when known.
type Location struct {
	Paragraph *int   `json:"paragraph,omitempty"`
	Cell      string `json:"cell,omitempty"`
	Slide     *int   `json:"slide,omitempty"`
	Row       *int   `json:"row,omitempty"`
}

// Problem is one problem reported by the engine.
type Problem struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
	// Part is the package part, e.g. "word/document.xml".
	Part string `json:"part,omitempty"`
	// Tag is the raw tag text, e.g. "{{#items}}".
	Tag      string    `json:"tag,omitempty"`
	Location *Location `json:"location,omitempty"`
	// Details holds extra data, e.g. "hostMessage" for failed filters or "limit".
	Details map[string]any `json:"details,omitempty"`
}

func (p Problem) String() string {
	var where []string
	if p.Tag != "" {
		where = append(where, "tag "+p.Tag)
	}
	if p.Part != "" {
		where = append(where, "in "+p.Part)
	}
	if len(where) == 0 {
		return p.Message
	}
	return p.Message + " (" + strings.Join(where, " ") + ")"
}

// Error is returned when the engine rejects a template, data, document or model. The embedded
// Problem is the first one; Problems lists all of them (compilation reports every template mistake
// at once).
type Error struct {
	Problem
	Problems []Problem
}

func newError(problems []Problem) *Error {
	if len(problems) == 0 {
		problems = []Problem{{Code: ErrInternal, Message: "Unknown engine error"}}
	}
	return &Error{Problem: problems[0], Problems: problems}
}

func errorf(code ErrorCode, format string, args ...any) *Error {
	return newError([]Problem{{Code: code, Message: fmt.Sprintf(format, args...)}})
}

func (e *Error) Error() string {
	s := "coffee: " + e.Problem.String()
	if n := len(e.Problems); n > 1 {
		s += fmt.Sprintf(" (+%d more)", n-1)
	}
	return s
}

// Is reports whether any of the problems has the code target, so that
// errors.Is(err, coffee.ErrUnknownFilter) works even when it is not the first problem.
func (e *Error) Is(target error) bool {
	code, ok := target.(ErrorCode)
	if !ok {
		return false
	}
	for _, p := range e.Problems {
		if p.Code == code {
			return true
		}
	}
	return false
}
