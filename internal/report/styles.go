package report

import "github.com/xuri/excelize/v2"

type ReportStyles struct {
	HeaderFont      int
	ThinBorder      int
	VCenter         int
	Center          int
	WrapVCenter     int
	WrapLeft        int
	PassedFill      int
	FailedFill      int
	SkippedFill     int
	BlackSkipFill   int
	UnsupportedFill int
	RunningSkipFill int
}

func newReportStyles(f *excelize.File) *ReportStyles {
	mk := func(s *excelize.Style) int {
		id, _ := f.NewStyle(s)
		return id
	}
	thin := []excelize.Border{{Type: "thin", Color: "000000", Style: 1}}
	return &ReportStyles{
		HeaderFont: mk(&excelize.Style{
			Font:      &excelize.Font{Bold: true, Size: 11, Color: "FFFFFF"},
			Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"4472C4"}},
			Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
			Border:    thin,
		}),
		ThinBorder:  mk(&excelize.Style{Border: thin}),
		VCenter:     mk(&excelize.Style{Alignment: &excelize.Alignment{Vertical: "center"}, Border: thin}),
		Center:      mk(&excelize.Style{Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"}, Border: thin}),
		WrapVCenter: mk(&excelize.Style{Alignment: &excelize.Alignment{Vertical: "center", WrapText: true}, Border: thin}),
		WrapLeft:    mk(&excelize.Style{Alignment: &excelize.Alignment{Horizontal: "left", Vertical: "center", WrapText: true}, Border: thin}),
		PassedFill: mk(&excelize.Style{
			Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"C6EFCE"}},
			Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
			Border:    thin,
		}),
		FailedFill: mk(&excelize.Style{
			Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"FFC7CE"}},
			Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
			Border:    thin,
		}),
		SkippedFill: mk(&excelize.Style{
			Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"FFEB9C"}},
			Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
			Border:    thin,
		}),
		BlackSkipFill: mk(&excelize.Style{
			Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"FFC000"}},
			Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
			Border:    thin,
		}),
		UnsupportedFill: mk(&excelize.Style{
			Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"FFC7CE"}},
			Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
			Border:    thin,
		}),
		RunningSkipFill: mk(&excelize.Style{
			Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"FFFF00"}},
			Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
			Border:    thin,
		}),
	}
}
