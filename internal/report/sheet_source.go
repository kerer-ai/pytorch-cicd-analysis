package report

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/xuri/excelize/v2"
)

// Google Sheets 是 summary 模板的唯一实时数据源：CI 运行时直接从该表格导出 xlsx，
// 再解析为 SummaryTemplate（替代写死的 conf/_summary_template_data.json）。
// 表格列为 A-I：A=Classification, B=Specialization, C=File, D=Status, E=Priority,
// F=Assignee, G=PRs(Ready for Review), H=PRs(Merged), I=Notes（无 J-P 统计列）。
const (
	googleSheetID        = "1cDNiLW4KvPcGYPlA3KCDm0zV5PLPUWubno1OyCznKBw"
	googleSheetExportURL = "https://docs.google.com/spreadsheets/d/" + googleSheetID + "/export?format=xlsx"
)

// LoadSummaryTemplateFromGoogle 从 Google Sheets 导出 xlsx 并解析为 SummaryTemplate。
// 使用 30s 超时的默认 client（http.ProxyFromEnvironment 自动遵循系统代理）。
func LoadSummaryTemplateFromGoogle() (*SummaryTemplate, error) {
	return LoadSummaryTemplateFromGoogleWithClient(&http.Client{Timeout: 30 * time.Second})
}

// LoadSummaryTemplateFromGoogleWithClient 使用指定 client 从 Google 下载并解析。
func LoadSummaryTemplateFromGoogleWithClient(client *http.Client) (*SummaryTemplate, error) {
	resp, err := client.Get(googleSheetExportURL)
	if err != nil {
		return nil, fmt.Errorf("download summary sheet: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download summary sheet: unexpected status %s", resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read summary sheet: %w", err)
	}
	return parseSummaryTemplateXLSX(data)
}

// parseSummaryTemplateXLSX 将 Google 导出的 xlsx 字节流解析为 SummaryTemplate：
// 逐 sheet 读入单元格（值+样式）、合并区域、列宽。单元格值统一按字符串读取
// （分类/文件/优先级等均为文本列）。
func parseSummaryTemplateXLSX(data []byte) (*SummaryTemplate, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("open summary xlsx: %w", err)
	}
	defer f.Close()

	tmpl := &SummaryTemplate{}
	for _, sheetName := range f.GetSheetList() {
		sheet := SummarySheet{
			Cells:        map[string]SummaryCell{},
			MergedCells:  []string{},
			ColumnWidths: map[string]float64{},
		}

		rows, err := f.GetRows(sheetName)
		if err != nil {
			return nil, fmt.Errorf("read sheet %q: %w", sheetName, err)
		}

		maxCol := 0
		for rIdx, row := range rows {
			for cIdx, val := range row {
				if val == "" {
					continue
				}
				if cIdx > maxCol {
					maxCol = cIdx
				}
				ref := fmt.Sprintf("%s%d", colLetterName(cIdx+1), rIdx+1)
				cell := SummaryCell{Value: val}
				if sid, serr := f.GetCellStyle(sheetName, ref); serr == nil {
					if st, sterr := f.GetStyle(sid); sterr == nil {
						applySummaryStyle(&cell, st)
					}
				}
				sheet.Cells[ref] = cell
			}
		}

		if merges, merr := f.GetMergeCells(sheetName); merr == nil {
			for _, m := range merges {
				start, end := m.GetStartAxis(), m.GetEndAxis()
				if start != "" && end != "" && start != end {
					sheet.MergedCells = append(sheet.MergedCells, start+":"+end)
				}
			}
		}

		for col := 0; col <= maxCol; col++ {
			name := colLetterName(col + 1)
			if w, werr := f.GetColWidth(sheetName, name); werr == nil && w > 0 {
				sheet.ColumnWidths[name] = w
			}
		}

		tmpl.Sheets = append(tmpl.Sheets, SummarySheetEntry{Name: sheetName, Sheet: sheet})
	}
	return tmpl, nil
}

// applySummaryStyle 将 excelize 读取的样式映射到 SummaryCell（与 summaryCellStyle
// 的写入逻辑对称，保证 summary_report.xlsx 复现 Google 表格的样式）。
func applySummaryStyle(cell *SummaryCell, st *excelize.Style) {
	if st.Font != nil {
		cell.FontBold = st.Font.Bold
		cell.FontSize = st.Font.Size
		cell.FontColor = st.Font.Color
	}
	if st.Fill.Pattern == 1 && len(st.Fill.Color) > 0 {
		cell.FillColor = st.Fill.Color[0]
	}
	if st.Alignment != nil {
		cell.AlignH = st.Alignment.Horizontal
		cell.AlignV = st.Alignment.Vertical
		if st.Alignment.WrapText {
			wt := true
			cell.WrapText = &wt
		}
	}
}