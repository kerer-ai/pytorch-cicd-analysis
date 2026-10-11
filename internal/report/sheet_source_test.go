package report

import (
	"testing"

	"github.com/xuri/excelize/v2"
)

// TestParseSummaryTemplateXLSX 验证 xlsx -> SummaryTemplate 解析：
// sheet 名、单元格值、合并区域、列宽、样式（加粗/填充/换行）均能正确还原。
func TestParseSummaryTemplateXLSX(t *testing.T) {
	f := excelize.NewFile()
	f.SetSheetName("Sheet1", "Core")
	f.SetCellValue("Core", "A1", "Classification")
	f.SetCellValue("Core", "B1", "Specialization")
	f.SetCellValue("Core", "C1", "File")
	f.SetCellValue("Core", "A2", "Core")
	f.SetCellValue("Core", "C2", "test/nn/test_a.py")

	styleID, err := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Size: 11},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"FFC6EFCE"}},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center", WrapText: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.SetCellStyle("Core", "A1", "A1", styleID)
	f.MergeCell("Core", "A2", "A3")
	f.SetColWidth("Core", "C", "C", 40)

	buf, err := f.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}

	tmpl, err := parseSummaryTemplateXLSX(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(tmpl.Sheets) != 1 {
		t.Fatalf("sheets = %d, want 1", len(tmpl.Sheets))
	}
	entry := tmpl.Sheets[0]
	if entry.Name != "Core" {
		t.Fatalf("sheet name = %q, want Core", entry.Name)
	}
	if v, _ := entry.Sheet.Cells["A2"].Value.(string); v != "Core" {
		t.Errorf("A2 = %q, want Core", v)
	}
	if v, _ := entry.Sheet.Cells["C2"].Value.(string); v != "test/nn/test_a.py" {
		t.Errorf("C2 = %q, want test/nn/test_a.py", v)
	}

	foundMerge := false
	for _, m := range entry.Sheet.MergedCells {
		if m == "A2:A3" {
			foundMerge = true
		}
	}
	if !foundMerge {
		t.Errorf("merged cells = %v, want A2:A3", entry.Sheet.MergedCells)
	}

	a1 := entry.Sheet.Cells["A1"]
	if !a1.FontBold || a1.FillColor != "FFC6EFCE" || a1.WrapText == nil || !*a1.WrapText {
		t.Errorf("A1 style = %+v, want bold+fill+wrap", a1)
	}

	if w := entry.Sheet.ColumnWidths["C"]; w != 40 {
		t.Errorf("C width = %v, want 40", w)
	}
}