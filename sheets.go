package coffee

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Ptr returns a pointer to v, for the optional fields of the model: DocxRunStyle{Bold: coffee.Ptr(true)}.
func Ptr[T any](v T) *T { return &v }

// ColumnToIndex converts a column name to its 1-based index: "A" → 1, "AA" → 27.
func ColumnToIndex(col string) int {
	n := 0
	for _, c := range strings.ToUpper(col) {
		n = n*26 + int(c-'A'+1)
	}
	return n
}

// IndexToColumn converts a 1-based column index to its name: 1 → "A", 27 → "AA".
func IndexToColumn(n int) string {
	var b []byte
	for n > 0 {
		m := (n - 1) % 26
		b = append([]byte{byte('A' + m)}, b...)
		n = (n - 1) / 26
	}
	return string(b)
}

// ParseRef splits an A1-style reference into 1-based column and row: "$B$3" → 2, 3.
func ParseRef(ref string) (col, row int, err error) {
	s := strings.TrimPrefix(ref, "$")
	i := 0
	for i < len(s) && (s[i] >= 'A' && s[i] <= 'Z' || s[i] >= 'a' && s[i] <= 'z') {
		i++
	}
	digits := strings.TrimPrefix(s[i:], "$")
	row, perr := strconv.Atoi(digits)
	if i == 0 || perr != nil || row < 1 || strings.HasPrefix(digits, "+") {
		return 0, 0, fmt.Errorf("coffee: invalid cell reference %q", ref)
	}
	return ColumnToIndex(s[:i]), row, nil
}

var (
	epoch1900 = time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	epoch1904 = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
)

const dayMs = 86400000

// SerialToDate converts an Excel serial date to a time in UTC.
func SerialToDate(serial float64, date1904 bool) time.Time {
	epoch := epoch1900
	if date1904 {
		epoch = epoch1904
	}
	return epoch.Add(time.Duration(math.Round(serial*dayMs)) * time.Millisecond)
}

// DateToSerial converts a time (its wall clock in UTC) to an Excel serial date.
func DateToSerial(t time.Time, date1904 bool) float64 {
	epoch := epoch1900
	if date1904 {
		epoch = epoch1904
	}
	return float64(t.Sub(epoch).Milliseconds()) / dayMs
}

// SheetToArrays returns the values of a sheet as rows of cells, nil for empty cells.
func SheetToArrays(sheet XlsxSheet) [][]any {
	maxRow, maxCol := 0, 0
	for _, r := range sheet.Rows {
		maxRow = max(maxRow, r.Index)
		col := 0
		for _, c := range r.Cells {
			col = cellColumn(c, col)
			maxCol = max(maxCol, col)
		}
	}
	out := make([][]any, maxRow)
	for i := range out {
		out[i] = make([]any, maxCol)
	}
	for _, r := range sheet.Rows {
		if r.Index < 1 {
			continue
		}
		col := 0
		for _, c := range r.Cells {
			col = cellColumn(c, col)
			if col >= 1 {
				out[r.Index-1][col-1] = c.Value
			}
		}
	}
	return out
}

func cellColumn(c XlsxCell, prev int) int {
	if c.Ref != "" {
		if col, _, err := ParseRef(c.Ref); err == nil {
			return col
		}
	}
	return prev + 1
}

// SheetToObjects returns the rows below headerRow (1-based) as maps keyed by the header cells.
// Empty header cells are named column1, column2…; empty rows are skipped.
func SheetToObjects(sheet XlsxSheet, headerRow int) []map[string]any {
	rows := SheetToArrays(sheet)
	if headerRow < 1 {
		headerRow = 1
	}
	var header []string
	if headerRow <= len(rows) {
		for i, h := range rows[headerRow-1] {
			s := ""
			if h != nil {
				s = fmt.Sprint(h)
			}
			if s == "" {
				s = "column" + strconv.Itoa(i+1)
			}
			header = append(header, s)
		}
	}
	var out []map[string]any
	for r := headerRow; r < len(rows); r++ {
		empty := true
		for _, v := range rows[r] {
			if v != nil && v != "" {
				empty = false
				break
			}
		}
		if empty {
			continue
		}
		obj := make(map[string]any, len(header))
		for i, h := range header {
			if i < len(rows[r]) {
				obj[h] = rows[r][i]
			} else {
				obj[h] = nil
			}
		}
		out = append(out, obj)
	}
	return out
}

// SheetFromArrays builds a new sheet from rows of values: strings, numbers, booleans, time.Time
// (written as date cells) and nil for empty cells.
func SheetFromArrays(name string, rows [][]any) XlsxSheet {
	sheet := XlsxSheet{Name: name, Rows: make([]XlsxRow, 0, len(rows))}
	for i, r := range rows {
		row := XlsxRow{Index: i + 1}
		for j, v := range r {
			if v == nil {
				continue
			}
			c := XlsxCell{Ref: IndexToColumn(j+1) + strconv.Itoa(i+1), Value: v}
			switch x := v.(type) {
			case time.Time:
				c.Value, c.Type = x.UTC().Format("2006-01-02T15:04:05.000Z"), "date"
			case Date:
				c.Value, c.Type = string(x), "date"
			}
			row.Cells = append(row.Cells, c)
		}
		sheet.Rows = append(sheet.Rows, row)
	}
	return sheet
}
