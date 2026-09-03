package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

// SummaryTemplate 是 summary_report.xlsx 的单元格级模板，移植自
// /tmp/22222 conf/_summary_template_data.json。
type SummaryTemplate struct {
	Sheets []SummarySheetEntry
}

type SummarySheetEntry struct {
	Name  string
	Sheet SummarySheet
}

type SummarySheet struct {
	Cells        map[string]SummaryCell `json:"cells"`
	MergedCells  []string               `json:"merged_cells"`
	ColumnWidths map[string]float64     `json:"column_widths"`
}

type SummaryCell struct {
	Value     interface{} `json:"value"`
	FontBold  bool        `json:"font_bold"`
	FontSize  float64     `json:"font_size"`
	FontColor string      `json:"font_color"`
	FillColor string      `json:"fill_color"`
	AlignH    string      `json:"align_h"`
	AlignV    string      `json:"align_v"`
	WrapText  *bool       `json:"wrap_text"`
}

// UnmarshalJSON 保留 JSON 对象键顺序（sheet 顺序对产物有影响）。
func (t *SummaryTemplate) UnmarshalJSON(data []byte) error {
	var top struct {
		Sheets json.RawMessage `json:"sheets"`
	}
	if err := json.Unmarshal(data, &top); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(top.Sheets))
	if _, err := dec.Token(); err != nil { // consume '{'
		return err
	}
	for dec.More() {
		nameTok, err := dec.Token()
		if err != nil {
			return err
		}
		name := nameTok.(string)
		var sheet SummarySheet
		if err := dec.Decode(&sheet); err != nil {
			return err
		}
		t.Sheets = append(t.Sheets, SummarySheetEntry{Name: name, Sheet: sheet})
	}
	_, err := dec.Token() // consume '}'
	return err
}

func LoadSummaryTemplate(path string) (*SummaryTemplate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t SummaryTemplate
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func LoadSummaryTemplateFS(fsys fs.FS, name string) (*SummaryTemplate, error) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, err
	}
	var t SummaryTemplate
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// summaryStatsSheets 是需要填充 K-O 运行时统计的 sheet（排除 README 类）。
var summaryStatsSheets = []string{"Core", "Tensor", "Distributed", "Graph", "Math", "Quantization", "Utils"}

func stripAlphaColor(c string) string {
	if len(c) == 8 {
		return c[2:]
	}
	return c
}

func setSummaryCellValue(f *excelize.File, sheet, ref string, v interface{}) {
	switch val := v.(type) {
	case nil:
		return
	case string:
		f.SetCellValue(sheet, ref, val)
	case float64:
		if val == math.Trunc(val) && !math.IsInf(val, 0) {
			f.SetCellValue(sheet, ref, int64(val))
		} else {
			f.SetCellValue(sheet, ref, val)
		}
	case bool:
		f.SetCellValue(sheet, ref, val)
	default:
		f.SetCellValue(sheet, ref, fmt.Sprintf("%v", val))
	}
}

func writeSummaryReport(path string, tmpl *SummaryTemplate, orderedFiles []fileGroup) error {
	fileStats := computeFileStats(orderedFiles)

	f := excelize.NewFile()
	styleCache := map[string]int{}

	for i, entry := range tmpl.Sheets {
		sheetName := entry.Name
		if i == 0 {
			f.SetSheetName("Sheet1", sheetName)
		} else {
			f.NewSheet(sheetName)
		}

		for col, w := range entry.Sheet.ColumnWidths {
			f.SetColWidth(sheetName, col, col, w)
		}

		for ref, cell := range entry.Sheet.Cells {
			setSummaryCellValue(f, sheetName, ref, cell.Value)
			if styleID := summaryCellStyle(f, &styleCache, cell); styleID != 0 {
				f.SetCellStyle(sheetName, ref, ref, styleID)
			}
		}

		for _, m := range entry.Sheet.MergedCells {
			parts := strings.SplitN(m, ":", 2)
			if len(parts) == 2 {
				f.MergeCell(sheetName, parts[0], parts[1])
			}
		}
	}

	fillSummaryStats(f, tmpl, fileStats, &styleCache)

	return f.SaveAs(path)
}

// computeFileStats 按 file 统计：failed 仅计 status=="failed"（error 不计入 N 列，
// 对齐 Python fill_summary_stats 的 file_stats 语义）。
func computeFileStats(orderedFiles []fileGroup) map[string]fileStat {
	stats := make(map[string]fileStat, len(orderedFiles))
	for _, fg := range orderedFiles {
		s := fileStat{}
		for _, tc := range fg.cases {
			s.total++
			switch tc.Status {
			case "passed":
				s.passed++
			case "failed":
				s.failed++
			case "skipped":
				s.skipped++
			}
		}
		stats[fg.path] = s
	}
	return stats
}

type fileStat struct {
	total, passed, failed, skipped int
}

func fillSummaryStats(f *excelize.File, tmpl *SummaryTemplate, fileStats map[string]fileStat, styleCache *map[string]int) {
	byName := map[string]*SummarySheetEntry{}
	for i := range tmpl.Sheets {
		byName[tmpl.Sheets[i].Name] = &tmpl.Sheets[i]
	}

	for _, sheetName := range summaryStatsSheets {
		entry, ok := byName[sheetName]
		if !ok {
			continue
		}
		for ref, cell := range entry.Sheet.Cells {
			col, row := splitCellRef(ref)
			if col != "C" || row < 2 {
				continue
			}
			fpath, _ := cell.Value.(string)
			if fpath == "" {
				continue
			}
			stat, ok := fileStats[fpath]
			if !ok {
				continue
			}
			successRate := "0.0%"
			if stat.total > 0 {
				successRate = fmt.Sprintf("%.1f%%", float64(stat.passed+stat.skipped)/float64(stat.total)*100)
			}
			f.SetCellValue(sheetName, fmt.Sprintf("K%d", row), stat.total)
			f.SetCellValue(sheetName, fmt.Sprintf("L%d", row), successRate)
			f.SetCellValue(sheetName, fmt.Sprintf("M%d", row), stat.passed)
			f.SetCellValue(sheetName, fmt.Sprintf("N%d", row), stat.failed)
			f.SetCellValue(sheetName, fmt.Sprintf("O%d", row), stat.skipped)

			for _, colLetter := range []string{"K", "L", "M", "N", "O"} {
				cellRef := fmt.Sprintf("%s%d", colLetter, row)
				f.SetCellStyle(sheetName, cellRef, cellRef, summaryCenterStyle(f, styleCache))
			}
		}
	}
}

func summaryCenterStyle(f *excelize.File, cache *map[string]int) int {
	const key = "center"
	if id, ok := (*cache)[key]; ok {
		return id
	}
	id, _ := f.NewStyle(&excelize.Style{
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
	})
	(*cache)[key] = id
	return id
}

func summaryCellStyle(f *excelize.File, cache *map[string]int, cell SummaryCell) int {
	key := fmt.Sprintf("%t|%g|%s|%s|%s|%s|%v", cell.FontBold, cell.FontSize, cell.FontColor, cell.FillColor, cell.AlignH, cell.AlignV, cell.WrapText)
	if id, ok := (*cache)[key]; ok {
		return id
	}

	style := &excelize.Style{}
	if cell.FontBold || cell.FontSize > 0 || (cell.FontColor != "" && cell.FontColor != "FF000000") {
		style.Font = &excelize.Font{}
		if cell.FontBold {
			style.Font.Bold = true
		}
		if cell.FontSize > 0 {
			style.Font.Size = cell.FontSize
		}
		if cell.FontColor != "" && cell.FontColor != "FF000000" {
			style.Font.Color = stripAlphaColor(cell.FontColor)
		}
	}
	if cell.FillColor != "" {
		style.Fill = excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{stripAlphaColor(cell.FillColor)}}
	}
	if cell.AlignH != "" || cell.AlignV != "" || (cell.WrapText != nil && *cell.WrapText) {
		al := &excelize.Alignment{}
		if cell.AlignH != "" {
			al.Horizontal = cell.AlignH
		}
		if cell.AlignV != "" {
			al.Vertical = cell.AlignV
		}
		if cell.WrapText != nil {
			al.WrapText = *cell.WrapText
		}
		style.Alignment = al
	}

	id, _ := f.NewStyle(style)
	(*cache)[key] = id
	return id
}

func splitCellRef(ref string) (string, int) {
	i := 0
	for i < len(ref) && ref[i] >= 'A' && ref[i] <= 'Z' {
		i++
	}
	col := ref[:i]
	row, _ := strconv.Atoi(ref[i:])
	return col, row
}
