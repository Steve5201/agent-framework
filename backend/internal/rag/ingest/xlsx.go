// xlsx.go —— Excel（.xlsx）解析（纯 Go，excelize，无需沙盒）。
//
// 每个工作表转成 Markdown 表格（表头 = 首行），作为独立"标题分段"
// （Heading = 工作表名），与 Markdown/PDF 等产物同一分块路径。
// 单元格内嵌公式只取计算值（教育场景成绩表/题库批量导入）。
package ingest

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/xuri/excelize/v2"
)

// parseXLSX 用 excelize 解析 .xlsx：每个 sheet → Markdown 表格段。
func parseXLSX(data []byte) (*ParsedDoc, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("xlsx 内容为空")
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("xlsx 打开失败: %w", err)
	}
	defer f.Close()

	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, fmt.Errorf("xlsx 无工作表")
	}
	doc := &ParsedDoc{Title: sheets[0]}
	for _, name := range sheets {
		rows, err := f.GetRows(name)
		if err != nil {
			return nil, fmt.Errorf("xlsx 读取工作表 %q 失败: %w", name, err)
		}
		var sb strings.Builder
		if len(rows) == 0 {
			continue
		}
		// 表头 + 分隔行（Markdown 表格）。
		head := sanitizeCells(rows[0])
		sb.WriteString("| " + strings.Join(head, " | ") + " |\n")
		sep := make([]string, len(head))
		for i := range sep {
			sep[i] = "---"
		}
		sb.WriteString("| " + strings.Join(sep, " | ") + " |\n")
		for _, row := range rows[1:] {
			cells := sanitizeCells(row)
			sb.WriteString("| " + strings.Join(cells, " | ") + " |\n")
		}
		doc.Segments = append(doc.Segments, Segment{
			Heading: name,
			Level:   1,
			Text:    sb.String(),
		})
	}
	return doc, nil
}

// sanitizeCells 清洗表格单元格：转义管道符、空单元格占位，避免破坏 Markdown 表格。
func sanitizeCells(row []string) []string {
	out := make([]string, len(row))
	for i, c := range row {
		c = strings.TrimSpace(c)
		c = strings.ReplaceAll(c, "|", "\\|")
		c = strings.ReplaceAll(c, "\n", " ")
		if c == "" {
			c = " "
		}
		out[i] = c
	}
	return out
}
