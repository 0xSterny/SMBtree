package loot

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/extrame/ole2"
	"github.com/extrame/xls"
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
	case ".doc":
		return convertDoc(path)
	case ".xlsx":
		return convertXlsx(path)
	case ".xls":
		return convertXls(path)
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

func convertDoc(path string) (string, error) {
	// Best-effort string extraction from OLE2 streams
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	ole, err := ole2.Open(f, "utf-8")
	if err != nil {
		return "", err
	}
	// ole.Close() might not exist if it just wraps file? extame/xls has Open that returns *WorkBook.
	// Let's assume ole2.Open matches. But compiler said `cannot use path ... as *os.File`. So it expects *os.File.
	// Also remove stream.Close()

	var buf bytes.Buffer
	buf.WriteString("--- Preview (Best Effort / Strings) ---\n")

	dirs, err := ole.ListDir()
	if err != nil {
		return "", err
	}

	for _, dir := range dirs {
		// We are looking for streams that might contain text.
		// WordDocument is usually the main stream.
		if dir.Type == 2 { // Stream
			stream := ole.OpenFile(dir, nil)
			// no deferred close

			// Read the stream and extract printable strings
			// Limit to avoiding massive memory usage on huge embedded objects
			data, err := io.ReadAll(io.LimitReader(stream, 1024*1024)) // 1MB limit per stream
			if err != nil {
				continue
			}

			// Simple string extraction: look for sequences of printable ascii or unicode
			// This is primitive but avoids needing a full Word binary parser
			extracted := extractStrings(data)
			if len(extracted) > 0 {
				buf.WriteString(fmt.Sprintf("\n[Stream: %s]\n", dir.Name()))
				buf.WriteString(extracted)
				buf.WriteString("\n")
			}
		}
	}

	if buf.Len() < 50 {
		return "", fmt.Errorf("no readable text found in .doc file")
	}

	return buf.String(), nil
}

func extractStrings(data []byte) string {
	var result bytes.Buffer
	var currentRun []rune

	minRunLength := 4

	for i := 0; i < len(data); i++ {
		// Check for simple ASCII
		char := rune(data[i])
		if unicode.IsPrint(char) && char < 127 {
			currentRun = append(currentRun, char)
		} else {
			// Check if we ended a valid run
			if len(currentRun) >= minRunLength {
				result.WriteString(string(currentRun))
				result.WriteString(" ")
			}
			currentRun = nil

			// Check for UTF-16LE (basic latin) often found in Word
			// Format: char 0x00
			if i+1 < len(data) && data[i+1] == 0 {
				possibleChar := rune(data[i])
				if unicode.IsPrint(possibleChar) && possibleChar < 127 {
					currentRun = append(currentRun, possibleChar)
					i++ // Skip the null byte
				}
			}
		}
	}
	// Flush last run
	if len(currentRun) >= minRunLength {
		result.WriteString(string(currentRun))
	}

	return result.String()
}

func convertXls(path string) (string, error) {
	f, err := xls.Open(path, "utf-8")
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	numSheets := f.NumSheets()
	for i := 0; i < numSheets; i++ {
		sheet := f.GetSheet(i)
		if sheet == nil {
			continue
		}
		buf.WriteString(fmt.Sprintf("\n--- Sheet: %s ---\n", sheet.Name))

		maxRow := int(sheet.MaxRow)
		for r := 0; r <= maxRow; r++ {
			row := sheet.Row(r)
			if row == nil {
				continue
			}

			var vals []string
			lastCol := row.LastCol()
			for c := 0; c < lastCol; c++ {
				vals = append(vals, row.Col(c))
			}
			buf.WriteString(strings.Join(vals, "\t") + "\n")
		}
	}
	return buf.String(), nil
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
