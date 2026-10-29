package core

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ledongthuc/pdf"
)

// documentExtensions lists every office/document extension we can extract.
var documentExtensions = map[string]bool{
	"pdf": true, "docx": true, "pptx": true, "xlsx": true,
	"odt": true, "odp": true, "ods": true,
	"doc": true, "ppt": true, "xls": true,
	"rtf": true, "epub": true,
}

var documentMIMEs = map[string]bool{
	"application/pdf":               true,
	"application/msword":            true,
	"application/vnd.ms-excel":      true,
	"application/vnd.ms-powerpoint": true,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   true,
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": true,
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         true,
	"application/vnd.oasis.opendocument.text":                                   true,
	"application/vnd.oasis.opendocument.presentation":                           true,
	"application/vnd.oasis.opendocument.spreadsheet":                            true,
	"application/rtf":      true,
	"application/epub+zip": true,
}

func extensionOf(name string) string {
	return strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
}

// IsDocument reports whether a MIME type belongs to a supported document.
func IsDocument(mimeType string) bool {
	return documentMIMEs[mimeType]
}

// IsAudio reports whether a MIME type is an audio format.
func IsAudio(mimeType string) bool {
	return strings.HasPrefix(mimeType, "audio/")
}

// IsExtractable reports whether a file name/MIME pair can be turned into text
// by a document extractor or the Ollama audio transcriber.
func IsExtractable(name, mimeType string) bool {
	ext := extensionOf(name)
	return documentExtensions[ext] || documentMIMEs[mimeType] ||
		audioExtensions[ext] || IsAudio(mimeType)
}

var audioExtensions = map[string]bool{
	"mp3": true, "wav": true, "ogg": true, "m4a": true, "flac": true, "opus": true,
}

// ExtractFileText extracts text from a document or transcribes an audio file
// using the legacy Ollama backend.
func ExtractFileText(ctx context.Context, name, mimeType string, data []byte, host, transcribeModel string) (string, string, error) {
	return ExtractFileTextWithAudio(ctx, name, mimeType, data, AudioOptions{
		Engine:      AudioEngineOllama,
		OllamaHost:  host,
		OllamaModel: transcribeModel,
	})
}

// ExtractFileTextWithAudio extracts a document or transcribes audio with the
// selected audio backend.
func ExtractFileTextWithAudio(ctx context.Context, name, mimeType string, data []byte, audio AudioOptions) (string, string, error) {
	ext := extensionOf(name)
	switch {
	case audioExtensions[ext] || IsAudio(mimeType):
		return audio.Transcribe(ctx, name, data)
	case documentExtensions[ext] || documentMIMEs[mimeType]:
		return ExtractDocument(ctx, name, data)
	default:
		return "", "", fmt.Errorf("no extractor for %s (%s)", name, mimeType)
	}
}

// ExtractDocument extracts text from a supported document. It returns the text
// and a short tool identifier such as "go:docx", "pdftotext" or "libreoffice".
func ExtractDocument(ctx context.Context, name string, data []byte) (string, string, error) {
	ext := extensionOf(name)

	var text, tool string
	var err error
	switch ext {
	case "docx":
		text, err = extractDOCX(data)
		tool = "go:docx"
	case "pptx":
		text, err = extractPPTX(data)
		tool = "go:pptx"
	case "xlsx":
		text, err = extractXLSX(data)
		tool = "go:xlsx"
	case "odt", "odp", "ods":
		text, err = extractODF(data)
		tool = "go:" + ext
	case "epub":
		text, err = extractEPUB(data)
		tool = "go:epub"
	case "rtf":
		text, err = extractRTF(data)
		tool = "go:rtf"
	case "pdf":
		return extractPDF(ctx, data)
	case "doc":
		if t, aerr := extractWithAntiword(ctx, data); aerr == nil && strings.TrimSpace(t) != "" {
			return cleanExtracted(t), "antiword", nil
		}
		return extractWithLibreOffice(ctx, name, data)
	case "ppt", "xls":
		return extractWithLibreOffice(ctx, name, data)
	default:
		return "", "", fmt.Errorf("unsupported document extension %q", ext)
	}

	if err == nil && strings.TrimSpace(text) != "" {
		return cleanExtracted(text), tool, nil
	}

	// A pure-Go parser may fail on unusual files (strict XML, encrypted ODF,
	// old generators). LibreOffice is a robust last resort when installed.
	loText, _, loErr := extractWithLibreOffice(ctx, name, data)
	if loErr == nil && strings.TrimSpace(loText) != "" {
		return cleanExtracted(loText), "libreoffice", nil
	}
	if err != nil {
		return "", tool, err
	}
	if loErr != nil {
		return "", tool, fmt.Errorf("no extractable text found; libreoffice fallback: %w", loErr)
	}
	return "", tool, errors.New("no extractable text found")
}

// ─── PDF ────────────────────────────────────────────────────────────────────

func extractPDF(ctx context.Context, data []byte) (string, string, error) {
	var pdfErr error
	if bin, err := exec.LookPath("pdftotext"); err == nil {
		out, runErr := runCommand(ctx, 2*time.Minute, bin,
			[]string{"-layout", "-enc", "UTF-8", "-", "-"}, data)
		if runErr == nil {
			if text := cleanExtracted(string(out)); text != "" {
				return text, "pdftotext", nil
			}
		} else {
			pdfErr = runErr
		}
	}

	text, err := extractPDFGo(data)
	if err == nil && strings.TrimSpace(text) != "" {
		return cleanExtracted(text), "go:pdf", nil
	}
	if pdfErr != nil {
		return "", "pdftotext", pdfErr
	}
	if err != nil {
		return "", "go:pdf", err
	}
	return "", "pdftotext", errors.New("no extractable text (possibly a scanned PDF; OCR is required)")
}

func extractPDFGo(data []byte) (string, error) {
	tmp, err := os.CreateTemp("", "noesis-pdf-*.pdf")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	defer os.Remove(path)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}

	f, r, err := pdf.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	reader, err := r.GetPlainText()
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(reader); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// ─── Office Open XML (docx/pptx/xlsx) ───────────────────────────────────────

func extractDOCX(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	names := []string{"word/document.xml", "word/footnotes.xml", "word/endnotes.xml"}
	for _, zf := range zr.File {
		if (strings.HasPrefix(zf.Name, "word/header") ||
			strings.HasPrefix(zf.Name, "word/footer") ||
			zf.Name == "word/comments.xml") && strings.HasSuffix(zf.Name, ".xml") {
			names = append(names, zf.Name)
		}
	}
	sort.Strings(names)

	var parts []string
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		raw, err := readZipEntry(zr, name)
		if err != nil {
			continue
		}
		text, err := extractXMLText(raw, xmlTextRules{
			textTags:  map[string]bool{"t": true},
			blockTags: map[string]bool{"p": true, "tr": true},
			breakTags: map[string]bool{"br": true, "cr": true},
			tabTags:   map[string]bool{"tab": true},
		})
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(text) != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 {
		return "", errors.New("docx: no document text found")
	}
	return strings.Join(parts, "\n\n"), nil
}

func extractPPTX(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	var slides []string
	for _, zf := range zr.File {
		name := zf.Name
		if !strings.HasPrefix(name, "ppt/slides/slide") || !strings.HasSuffix(name, ".xml") {
			continue
		}
		slides = append(slides, name)
	}
	sort.Slice(slides, func(i, j int) bool { return naturalLess(slides[i], slides[j]) })
	if len(slides) == 0 {
		return "", errors.New("pptx: no slides found")
	}

	var parts []string
	for _, name := range slides {
		raw, err := readZipEntry(zr, name)
		if err != nil {
			continue
		}
		text, err := extractXMLText(raw, xmlTextRules{
			textTags:  map[string]bool{"t": true},
			blockTags: map[string]bool{"p": true},
			breakTags: map[string]bool{"br": true},
		})
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(text) != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 {
		return "", errors.New("pptx: no slide text found")
	}
	return strings.Join(parts, "\n\n"), nil
}

func extractXLSX(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	shared, _ := readZipEntry(zr, "xl/sharedStrings.xml")
	stringsList := parseSharedStrings(shared)

	var sheets []string
	for _, zf := range zr.File {
		name := zf.Name
		if strings.HasPrefix(name, "xl/worksheets/sheet") && strings.HasSuffix(name, ".xml") {
			sheets = append(sheets, name)
		}
	}
	sort.Slice(sheets, func(i, j int) bool { return naturalLess(sheets[i], sheets[j]) })
	if len(sheets) == 0 {
		return "", errors.New("xlsx: no worksheets found")
	}

	var parts []string
	for _, name := range sheets {
		raw, err := readZipEntry(zr, name)
		if err != nil {
			continue
		}
		text, err := parseWorksheet(raw, stringsList)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(text) != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 {
		return "", errors.New("xlsx: no cell text found")
	}
	return strings.Join(parts, "\n\n"), nil
}

func parseSharedStrings(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.Strict = false
	var out []string
	var current strings.Builder
	inSI, inT := false, 0
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				inSI = true
				current.Reset()
			case "t":
				if inSI {
					inT++
				}
			}
		case xml.CharData:
			if inSI && inT > 0 {
				current.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				if inT > 0 {
					inT--
				}
			case "si":
				if inSI {
					out = append(out, current.String())
					inSI = false
				}
			}
		}
	}
	return out
}

type xlsxCell struct {
	Ref  string `xml:"r,attr"`
	Type string `xml:"t,attr"`
	V    string `xml:"v"`
	IS   struct {
		T string `xml:"t"`
	} `xml:"is"`
	R []struct {
		T string `xml:"t"`
	} `xml:"r"`
}

type xlsxRow struct {
	Cells []xlsxCell `xml:"c"`
}

type xlsxSheet struct {
	Rows []xlsxRow `xml:"sheetData>row"`
}

func parseWorksheet(raw []byte, shared []string) (string, error) {
	var sheet xlsxSheet
	if err := xml.Unmarshal(raw, &sheet); err != nil {
		return "", err
	}
	var rows []string
	for _, row := range sheet.Rows {
		var cells []string
		for _, cell := range row.Cells {
			value := ""
			switch cell.Type {
			case "s":
				if idx, err := strconv.Atoi(strings.TrimSpace(cell.V)); err == nil && idx >= 0 && idx < len(shared) {
					value = shared[idx]
				}
			case "inlineStr":
				value = cell.IS.T
				if value == "" {
					var b strings.Builder
					for _, r := range cell.R {
						b.WriteString(r.T)
					}
					value = b.String()
				}
			default:
				value = cell.V
			}
			cells = append(cells, value)
		}
		line := strings.TrimRight(strings.Join(cells, "\t"), "\t")
		if strings.TrimSpace(line) != "" {
			rows = append(rows, line)
		}
	}
	return strings.Join(rows, "\n"), nil
}

// ─── OpenDocument (odt/odp/ods) ─────────────────────────────────────────────

func extractODF(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	raw, err := readZipEntry(zr, "content.xml")
	if err != nil {
		return "", fmt.Errorf("content.xml: %w", err)
	}
	text, err := extractXMLText(raw, xmlTextRules{
		textTags: map[string]bool{
			"p": true, "h": true, "span": true, "a": true,
			"table-cell": true, "title": true, "subject": true,
		},
		blockTags: map[string]bool{"p": true, "h": true, "table-row": true},
		breakTags: map[string]bool{"line-break": true},
		tabTags:   map[string]bool{"tab": true},
		spaceTags: map[string]bool{"s": true},
	})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "", errors.New("odf: no text found in content.xml")
	}
	return text, nil
}

// ─── EPUB ───────────────────────────────────────────────────────────────────

func extractEPUB(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}

	var htmlFiles []string
	opfPath := epubOPFPath(zr)
	if opfPath != "" {
		if raw, err := readZipEntry(zr, opfPath); err == nil {
			htmlFiles = epubSpineFiles(zr, opfPath, raw)
		}
	}
	if len(htmlFiles) == 0 {
		for _, zf := range zr.File {
			ext := extensionOf(zf.Name)
			if ext == "xhtml" || ext == "html" || ext == "htm" {
				htmlFiles = append(htmlFiles, zf.Name)
			}
		}
		sort.Strings(htmlFiles)
	}
	if len(htmlFiles) == 0 {
		return "", errors.New("epub: no HTML/XHTML content found")
	}

	var parts []string
	for _, name := range htmlFiles {
		raw, err := readZipEntry(zr, name)
		if err != nil {
			continue
		}
		text := extractHTMLText(raw)
		if strings.TrimSpace(text) != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 {
		return "", errors.New("epub: no readable text found")
	}
	return strings.Join(parts, "\n\n"), nil
}

func epubOPFPath(zr *zip.Reader) string {
	raw, err := readZipEntry(zr, "META-INF/container.xml")
	if err != nil {
		return ""
	}
	var container struct {
		Rootfiles []struct {
			FullPath string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if xml.Unmarshal(raw, &container) != nil {
		return ""
	}
	for _, rf := range container.Rootfiles {
		if rf.FullPath != "" {
			return rf.FullPath
		}
	}
	return ""
}

func epubSpineFiles(zr *zip.Reader, opfPath string, raw []byte) []string {
	var opf struct {
		Manifest []struct {
			ID        string `xml:"id,attr"`
			Href      string `xml:"href,attr"`
			MediaType string `xml:"media-type,attr"`
		} `xml:"manifest>item"`
		Spine []struct {
			IDRef string `xml:"idref,attr"`
		} `xml:"spine>itemref"`
	}
	if xml.Unmarshal(raw, &opf) != nil {
		return nil
	}
	base := path.Dir(opfPath)
	byID := make(map[string]string, len(opf.Manifest))
	for _, item := range opf.Manifest {
		href := item.Href
		if decoded, err := url.PathUnescape(href); err == nil {
			href = decoded
		}
		if !strings.HasPrefix(href, "/") {
			href = path.Join(base, href)
		}
		href = strings.TrimPrefix(path.Clean(href), "/")
		byID[item.ID] = href
	}
	var out []string
	for _, ref := range opf.Spine {
		if href := byID[ref.IDRef]; href != "" {
			out = append(out, href)
		}
	}
	return out
}

func extractHTMLText(data []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	dec.AutoClose = xml.HTMLAutoClose
	dec.Entity = xml.HTMLEntity

	var b strings.Builder
	skip := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return stripHTML(string(data))
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch strings.ToLower(t.Name.Local) {
			case "head", "script", "style":
				skip++
			case "br":
				if skip == 0 {
					b.WriteByte('\n')
				}
			}
		case xml.EndElement:
			switch strings.ToLower(t.Name.Local) {
			case "head", "script", "style":
				if skip > 0 {
					skip--
				}
			case "p", "div", "h1", "h2", "h3", "h4", "h5", "h6", "li", "tr", "blockquote":
				if skip == 0 {
					b.WriteByte('\n')
				}
			}
		case xml.CharData:
			if skip == 0 {
				b.Write(t)
			}
		}
	}
	return cleanExtracted(b.String())
}

var (
	reHTMLScript = regexp.MustCompile(`(?is)<script\b.*?</script>|<style\b.*?</style>|<head\b.*?</head>`)
	reHTMLTag    = regexp.MustCompile(`(?s)<[^>]*>`)
)

func stripHTML(s string) string {
	s = reHTMLScript.ReplaceAllString(s, " ")
	s = reHTMLTag.ReplaceAllString(s, " ")
	s = strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'").Replace(s)
	return cleanExtracted(s)
}

// ─── RTF ────────────────────────────────────────────────────────────────────

var rtfSkipDestinations = map[string]bool{
	"fonttbl": true, "colortbl": true, "stylesheet": true, "info": true,
	"pict": true, "object": true, "header": true, "footer": true,
	"headerl": true, "headerr": true, "footerl": true, "footerr": true,
	"footnote": true, "annotation": true, "listtable": true,
	"listoverridetable": true, "rsidtbl": true, "generator": true,
	"xmlnstbl": true, "themedata": true, "colorschememapping": true,
	"latentstyles": true, "datastore": true, "mmathPr": true, "pgptbl": true,
	"filetbl": true, "revtbl": true, "userprops": true, "docvar": true,
	"bkmkstart": true, "bkmkend": true, "fldinst": true,
}

type rtfState struct {
	skip bool
	uc   int
}

func extractRTF(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("rtf: empty file")
	}
	s := string(data)
	if !strings.HasPrefix(strings.TrimSpace(s), "{\\rtf") {
		return "", errors.New("rtf: missing RTF header")
	}

	state := rtfState{uc: 1}
	stack := []rtfState{}
	var out strings.Builder
	skipChars := 0

	flushByte := func(b byte) {
		if state.skip {
			return
		}
		if skipChars > 0 {
			skipChars--
			return
		}
		out.WriteRune(cp1252Rune(b))
	}
	writeString := func(v string) {
		if !state.skip && skipChars == 0 {
			out.WriteString(v)
		}
	}

	for i := 0; i < len(s); {
		c := s[i]
		switch c {
		case '{':
			stack = append(stack, state)
			i++
		case '}':
			if len(stack) > 0 {
				state = stack[len(stack)-1]
				stack = stack[:len(stack)-1]
			}
			i++
		case '\\':
			if i+1 >= len(s) {
				i++
				continue
			}
			next := s[i+1]
			switch {
			case next == '\\' || next == '{' || next == '}':
				if !state.skip && skipChars == 0 {
					out.WriteByte(next)
				}
				i += 2
			case next == '\'':
				if i+3 < len(s) {
					if v, err := strconv.ParseUint(s[i+2:i+4], 16, 8); err == nil {
						flushByte(byte(v))
					}
					i += 4
				} else {
					i = len(s)
				}
			case next == '*':
				state.skip = true
				i += 2
			case next == '~':
				writeString(" ")
				i += 2
			case next == '_':
				writeString("-")
				i += 2
			case next == '-' || next == '|':
				i += 2
			case (next >= 'a' && next <= 'z') || (next >= 'A' && next <= 'Z'):
				j := i + 1
				for j < len(s) && ((s[j] >= 'a' && s[j] <= 'z') || (s[j] >= 'A' && s[j] <= 'Z')) {
					j++
				}
				word := s[i+1 : j]
				param := ""
				if j < len(s) && (s[j] == '-' || (s[j] >= '0' && s[j] <= '9')) {
					k := j
					if s[k] == '-' {
						k++
					}
					for k < len(s) && s[k] >= '0' && s[k] <= '9' {
						k++
					}
					param = s[j:k]
					j = k
				}
				if j < len(s) && s[j] == ' ' {
					j++
				}
				switch word {
				case "par", "line", "sect", "page":
					writeString("\n")
				case "tab", "cell":
					writeString("\t")
				case "row":
					writeString("\n")
				case "emdash":
					writeString("—")
				case "endash":
					writeString("–")
				case "bullet":
					writeString("•")
				case "lquote":
					writeString("‘")
				case "rquote":
					writeString("’")
				case "ldblquote":
					writeString("“")
				case "rdblquote":
					writeString("”")
				case "emspace", "enspace":
					writeString(" ")
				case "uc":
					if n, err := strconv.Atoi(param); err == nil {
						state.uc = n
					}
				case "u":
					if n, err := strconv.Atoi(param); err == nil {
						if n < 0 {
							n += 65536
						}
						writeString(string(rune(n)))
						skipChars = state.uc
					}
				case "bin":
					if n, err := strconv.Atoi(param); err == nil && n > 0 && j+n <= len(s) {
						j += n
					}
				default:
					if rtfSkipDestinations[word] {
						state.skip = true
					}
				}
				i = j
			default:
				i += 2
			}
		case '\r':
			if !state.skip {
				out.WriteByte('\n')
			}
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
			i++
		case '\n':
			if !state.skip {
				out.WriteByte('\n')
			}
			i++
		default:
			if !state.skip && skipChars == 0 {
				out.WriteByte(c)
			}
			i++
		}
	}
	text := cleanExtracted(out.String())
	if text == "" {
		return "", errors.New("rtf: no text found")
	}
	return text, nil
}

// cp1252Rune converts the Windows-1252 bytes commonly used by RTF. Most bytes
// are identical to Unicode; the 0x80–0x9F range differs.
func cp1252Rune(b byte) rune {
	if b < 0x80 || b >= 0xA0 {
		return rune(b)
	}
	table := [32]rune{
		'€', '\u0081', '‚', 'ƒ', '„', '…', '†', '‡',
		'ˆ', '‰', 'Š', '‹', 'Œ', '\u008d', 'Ž', '\u008f',
		'\u0090', '‘', '’', '“', '”', '•', '–', '—',
		'˜', '™', 'š', '›', 'œ', '\u009d', 'ž', 'Ÿ',
	}
	return table[b-0x80]
}

// ─── generic XML helpers ────────────────────────────────────────────────────

type xmlTextRules struct {
	textTags  map[string]bool
	blockTags map[string]bool
	breakTags map[string]bool
	tabTags   map[string]bool
	spaceTags map[string]bool
}

func extractXMLText(raw []byte, rules xmlTextRules) (string, error) {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.Strict = false
	var b strings.Builder
	textDepth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			local := t.Name.Local
			if rules.textTags[local] {
				textDepth++
			}
			if rules.breakTags[local] {
				b.WriteByte('\n')
			}
			if rules.tabTags[local] {
				b.WriteByte('\t')
			}
			if rules.spaceTags[local] {
				n := 1
				for _, a := range t.Attr {
					if a.Name.Local == "c" {
						if v, err := strconv.Atoi(a.Value); err == nil && v > 0 {
							n = v
						}
					}
				}
				b.WriteString(strings.Repeat(" ", n))
			}
		case xml.CharData:
			if textDepth > 0 {
				b.Write(t)
			}
		case xml.EndElement:
			local := t.Name.Local
			if rules.textTags[local] && textDepth > 0 {
				textDepth--
			}
			if rules.blockTags[local] {
				b.WriteByte('\n')
			}
		}
	}
	return b.String(), nil
}

func readZipEntry(zr *zip.Reader, name string) ([]byte, error) {
	for _, zf := range zr.File {
		if zf.Name != name {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}
	return nil, fmt.Errorf("zip entry %q not found", name)
}

// naturalLess compares strings so that slide2.xml sorts before slide10.xml.
func naturalLess(a, b string) bool {
	na := naturalParts(a)
	nb := naturalParts(b)
	for i := 0; i < len(na) && i < len(nb); i++ {
		if na[i] == nb[i] {
			continue
		}
		ai, aerr := strconv.Atoi(na[i])
		bi, berr := strconv.Atoi(nb[i])
		if aerr == nil && berr == nil {
			return ai < bi
		}
		return na[i] < nb[i]
	}
	return len(na) < len(nb)
}

var reNaturalParts = regexp.MustCompile(`\d+|\D+`)

func naturalParts(s string) []string {
	return reNaturalParts.FindAllString(s, -1)
}

// ─── external tools ─────────────────────────────────────────────────────────

func extractWithAntiword(ctx context.Context, data []byte) (string, error) {
	bin, err := exec.LookPath("antiword")
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "noesis-antiword-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	in := filepath.Join(dir, "input.doc")
	if err := os.WriteFile(in, data, 0o600); err != nil {
		return "", err
	}
	out, err := runCommand(ctx, time.Minute, bin, []string{"-w", "0", "-m", "UTF-8.txt", in}, nil)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func extractWithLibreOffice(ctx context.Context, name string, data []byte) (string, string, error) {
	bin := findLibreOffice()
	if bin == "" {
		return "", "", errors.New("libreoffice/soffice is not installed")
	}
	ext := extensionOf(name)
	if ext == "" {
		ext = "bin"
	}
	dir, err := os.MkdirTemp("", "noesis-lo-*")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(dir)

	in := filepath.Join(dir, "input."+ext)
	if err := os.WriteFile(in, data, 0o600); err != nil {
		return "", "", err
	}
	outDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", "", err
	}
	target := "txt"
	if ext == "xls" || ext == "xlsx" || ext == "ods" {
		target = "csv"
	}
	profile := (&url.URL{Scheme: "file", Path: filepath.Join(dir, "profile")}).String()
	args := []string{
		"--headless", "--norestore", "--invisible", "--nologo",
		"-env:UserInstallation=" + profile,
		"--convert-to", target,
		"--outdir", outDir,
		in,
	}
	if _, err := runCommand(ctx, 3*time.Minute, bin, args, nil); err != nil {
		return "", "", err
	}

	entries, err := os.ReadDir(outDir)
	if err != nil {
		return "", "", err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if extensionOf(e.Name()) != target {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(outDir, e.Name()))
		if err != nil {
			return "", "", err
		}
		return string(raw), "libreoffice", nil
	}
	return "", "", fmt.Errorf("libreoffice produced no .%s output", target)
}

func findLibreOffice() string {
	for _, name := range []string{"libreoffice", "soffice"} {
		if bin, err := exec.LookPath(name); err == nil {
			return bin
		}
	}
	return ""
}

func runCommand(ctx context.Context, timeout time.Duration, name string, args []string, stdin []byte) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if cctx.Err() == context.DeadlineExceeded {
		return stdout.Bytes(), fmt.Errorf("%s timed out after %s", filepath.Base(name), timeout)
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 500 {
			msg = msg[len(msg)-500:]
		}
		if msg != "" {
			return stdout.Bytes(), fmt.Errorf("%s: %w: %s", filepath.Base(name), err, msg)
		}
		return stdout.Bytes(), fmt.Errorf("%s: %w", filepath.Base(name), err)
	}
	return stdout.Bytes(), nil
}

// cleanExtracted normalises line endings and removes excessive blank lines.
func cleanExtracted(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\x00", "")
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	s = strings.Join(lines, "\n")
	s = regexp.MustCompile(`\n{3,}`).ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// ─── re-extraction ──────────────────────────────────────────────────────────

type extractCandidate struct {
	id      int64
	path    string
	name    string
	abs     string
	blob    string
	content string
	mime    string
	text    string
	err     string
}

// ExtractAll re-runs document extraction, audio transcription, and image
// description for every stored note that supports it. filter optionally
// narrows the run by file name and/or format. Existing extracted text is kept
// when a re-extraction fails; the error is stored in extract_error.
func ExtractAll(ctx context.Context, r Reporter, db *sql.DB, c Config, filter ExtractFilter, force bool) error {
	rows, err := db.Query(`SELECT id, file_path, file_name, abs_path, blob_path, content, mime_type,
		markdown, extract_error FROM notes ORDER BY id`)
	if err != nil {
		return err
	}
	var candidates []extractCandidate
	for rows.Next() {
		var n extractCandidate
		if err := rows.Scan(&n.id, &n.path, &n.name, &n.abs, &n.blob, &n.content, &n.mime,
			&n.text, &n.err); err != nil {
			rows.Close()
			return err
		}
		// Include extractable documents/audio AND images (for vision description).
		if !CanExtract(n.name, n.mime) {
			continue
		}
		if !filter.Match(n.path, n.name, n.mime) {
			continue
		}
		if !force && n.text != "" && n.err == "" {
			continue
		}
		candidates = append(candidates, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	workers := normalizedWorkers(c.Workers)
	label := "extract documents, images & audio"
	if c.AudioEngine != "" {
		label += " (" + AudioEngineLabel(c.AudioEngine) + ")"
	}
	label += fmt.Sprintf(" ×%d", workers)
	r.SetPhase(label, 0, len(candidates))
	if len(candidates) == 0 {
		r.Logf("nothing to extract")
		return nil
	}

	var mu sync.Mutex
	ok, failed := 0, 0
	err = runConcurrent(ctx, workers, candidates, func(n extractCandidate) error {
		r.SetCurrent(n.name)
		data, err := noteBytes(db, n.abs, n.blob, n.content, n.mime)
		if err != nil {
			saveMu.Lock()
			_, _ = db.Exec(`UPDATE notes SET extract_error=?, extract_tool='' WHERE id=?`, err.Error(), n.id)
			saveMu.Unlock()
			mu.Lock()
			failed++
			mu.Unlock()
			r.Fail("%s: %v", n.name, err)
			r.Advance(false)
			return nil
		}

		text, tool, err := ExtractNoteText(ctx, c, db, n.name, n.mime, data, n.id)
		if err != nil {
			saveMu.Lock()
			_, _ = db.Exec(`UPDATE notes SET extract_error=?, extract_tool=? WHERE id=?`, err.Error(), tool, n.id)
			saveMu.Unlock()
			mu.Lock()
			failed++
			mu.Unlock()
			r.Fail("%s: %v", n.name, err)
			r.Advance(false)
			return nil
		}
		text = cleanExtracted(text)
		if strings.TrimSpace(text) == "" {
			saveMu.Lock()
			_, _ = db.Exec(`UPDATE notes SET extract_error=? WHERE id=?`, "no text extracted", n.id)
			saveMu.Unlock()
			mu.Lock()
			failed++
			mu.Unlock()
			r.Fail("%s: no text extracted", n.name)
			r.Advance(false)
			return nil
		}
		saveMu.Lock()
		_, writeErr := db.Exec(`UPDATE notes SET markdown=?, extract_error='', extract_tool=? WHERE id=?`, text, tool, n.id)
		if writeErr == nil {
			_, _ = db.Exec(`DELETE FROM process_output WHERE note_id=? AND process_name=?`, n.id, DataExtractionProcess)
		}
		saveMu.Unlock()
		if writeErr != nil {
			mu.Lock()
			failed++
			mu.Unlock()
			r.Fail("%s: %v", n.name, writeErr)
			r.Advance(false)
			return nil
		}
		mu.Lock()
		ok++
		mu.Unlock()
		r.Logf("%s: extracted with %s", n.name, tool)
		r.Advance(false)
		return nil
	})
	if err != nil {
		return err
	}
	r.Logf("extraction: ok — %d, failed — %d", ok, failed)
	return nil
}

// noteBytes returns the original bytes for a note. Filesystem imports link to
// the original file (abs), archive/JSON imports link to a materialised blob, and
// legacy databases may still hold base64 content inline.
func noteBytes(db *sql.DB, abs, blob, content, mimeType string) ([]byte, error) {
	paths := []string{abs}
	if p := storedPath(db, blob); p != "" {
		paths = append(paths, p)
	}
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if data, err := os.ReadFile(p); err == nil {
			return data, nil
		}
	}
	if IsText(mimeType) {
		return []byte(content), nil
	}
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("no source file or inline content for %s", mimeType)
	}
	data, err := base64.StdEncoding.DecodeString(content)
	if err != nil {
		return nil, fmt.Errorf("decode stored base64 content: %w", err)
	}
	return data, nil
}
