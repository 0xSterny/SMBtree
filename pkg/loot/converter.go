package loot

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ledongthuc/pdf"
	"github.com/nguyenthenguyen/docx"
	"github.com/xuri/excelize/v2"
)

// ConvertFile reads a file and attempts to convert it to a readable string
func ConvertFile(path string) (string, error) {
	ext := strings.ToLower(filepath.Ext(path))

	switch ext {
	case ".txt", ".md", ".json", ".go", ".py", ".yaml", ".yml", ".xml", ".ini", ".conf", ".sh", ".bat", ".ps1":
		content, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return string(content), nil
	case ".pdf":
		return convertPDF(path)
	case ".docx":
		return convertDocx(path)
	case ".xlsx":
		return convertXlsx(path)
	default:
		// Fallback to Hex Dump
		return hexDump(path)
	}
}

func convertPDF(path string) (string, error) {
	f, r, err := pdf.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var buf bytes.Buffer
	totalPage := r.NumPage()

	for pageIndex := 1; pageIndex <= totalPage; pageIndex++ {
		p := r.Page(pageIndex)
		if p.V.IsNull() {
			continue
		}
		text, _ := p.GetPlainText(nil)
		buf.WriteString(fmt.Sprintf("\n--- Page %d ---\n", pageIndex))
		buf.WriteString(text)
	}
	return buf.String(), nil
}

func convertDocx(path string) (string, error) {
	r, err := docx.ReadDocxFile(path)
	if err != nil {
		return "", err
	}
	defer r.Close()

	content := r.Editable().GetContent()
	return content, nil
}

func convertXlsx(path string) (string, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var buf bytes.Buffer
	for _, sheet := range f.GetSheetList() {
		buf.WriteString(fmt.Sprintf("\n--- Sheet: %s ---\n", sheet))
		rows, err := f.GetRows(sheet)
		if err != nil {
			continue
		}
		for _, row := range rows {
			buf.WriteString(strings.Join(row, "\t") + "\n")
		}
	}
	return buf.String(), nil
}

func hexDump(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	// Read first 2KB for preview
	header := make([]byte, 2048)
	n, err := f.Read(header)
	if err != nil && err != io.EOF {
		return "", err
	}

	output := fmt.Sprintf("Binary File or Unsupported Format: %s\n", path)
	output += fmt.Sprintf("Displaying first %d bytes:\n\n", n)
	output += hex.Dump(header[:n])

	if n == 2048 {
		output += "\n... (truncated)"
	}
	return output, nil
}
