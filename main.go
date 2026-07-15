package main

import (
	"archive/zip"
	"bytes"
	"embed"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

//go:embed web template.xlsx
var embedded embed.FS

const version = "1.0.14"

type Guest struct {
	FullName       string `json:"fullName"`
	BirthDate      string `json:"birthDate"`
	BirthPrecision string `json:"birthPrecision"`
	Gender         string `json:"gender"`
	Nationality    string `json:"nationality"`
	Passport       string `json:"passport"`
	Room           string `json:"room"`
	Arrival        string `json:"arrival"`
	Departure      string `json:"departure"`
	Checkout       string `json:"checkout"`
}

type ImportResult struct {
	Records    []Guest `json:"records"`
	Warning    string  `json:"warning,omitempty"`
	Confidence *int    `json:"confidence,omitempty"`
}

type appServer struct {
	srv      *http.Server
	lastPing atomic.Int64
}

func main() {
	if runtime.GOOS == "windows" {
		setDPIAware()
	}
	logPath := filepath.Join(os.Getenv("LOCALAPPDATA"), "XNC Ocean", "XNC_Ocean.log")
	if os.Getenv("LOCALAPPDATA") == "" {
		logPath = filepath.Join(os.TempDir(), "XNC_Ocean.log")
	}
	_ = os.MkdirAll(filepath.Dir(logPath), 0755)
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
		log.SetOutput(f)
		defer f.Close()
	}
	log.Printf("XNC Ocean %s starting", version)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatalDialog("Không thể khởi động máy chủ nội bộ: " + err.Error())
		return
	}
	st := &appServer{}
	mux := http.NewServeMux()
	st.routes(mux)
	st.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 20 * time.Second}
	url := "http://" + ln.Addr().String() + "/"
	go func() {
		if err := st.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("server: %v", err)
		}
	}()
	time.Sleep(80 * time.Millisecond)
	st.lastPing.Store(time.Now().Unix())
	if err := runAppWindow(url); err != nil {
		log.Printf("embedded WebView2 failed: %v", err)
		fatalDialog("Không thể mở giao diện WebView2 nhúng.\n\n" + err.Error())
	}
	_ = st.srv.Close()
}

func (s *appServer) routes(mux *http.ServeMux) {
	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) {
		s.lastPing.Store(time.Now().Unix())
		writeJSON(w, map[string]any{"ok": true, "version": version})
	})
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]string{"version": version}) })
	mux.HandleFunc("/api/reference", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"countries": map[string]string{}})
	})
	mux.HandleFunc("/api/import", s.handleImport)
	mux.HandleFunc("/api/export/excel", s.handleExportExcel)
	mux.HandleFunc("/api/shutdown", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
		go func() { time.Sleep(250 * time.Millisecond); _ = s.srv.Close() }()
	})
	webfs, _ := fs.Sub(embedded, "web")
	mux.Handle("/", http.FileServer(http.FS(webfs)))
}

func (s *appServer) handleImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 80<<20)
	if err := r.ParseMultipartForm(80 << 20); err != nil {
		writeErr(w, 400, "File quá lớn hoặc không hợp lệ: "+err.Error())
		return
	}
	f, h, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "Không nhận được file")
		return
	}
	defer f.Close()
	mode := strings.ToLower(strings.TrimSpace(r.FormValue("mode")))
	tempDir, err := os.MkdirTemp("", "xnc-import-")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer os.RemoveAll(tempDir)
	name := sanitizeName(h.Filename)
	path := filepath.Join(tempDir, name)
	out, err := os.Create(path)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_, err = io.Copy(out, f)
	out.Close()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	ext := strings.ToLower(filepath.Ext(name))
	if mode == "auto" || mode == "" {
		switch ext {
		case ".xlsx", ".xls", ".csv":
			mode = "excel"
		case ".pdf":
			mode = "pdf"
		case ".docx", ".doc":
			mode = "word"
		case ".jpg", ".jpeg", ".png", ".bmp", ".tif", ".tiff":
			mode = "table"
		}
	}
	var recs []Guest
	var warn string
	switch mode {
	case "excel":
		recs, err = readSpreadsheet(path)
	case "word":
		recs, warn, err = readWord(path, tempDir)
	case "pdf":
		recs, warn, err = readPDF(path, tempDir)
	case "passport":
		recs, warn, err = readPassport(path, tempDir)
	case "table", "table-image":
		recs, warn, err = readTableImage(path, tempDir)
	default:
		err = fmt.Errorf("định dạng chưa được hỗ trợ: %s", ext)
	}
	if err != nil {
		log.Printf("import %s %s: %v", mode, name, err)
		writeErr(w, 500, err.Error())
		return
	}
	for i := range recs {
		normalizeGuest(&recs[i])
	}
	writeJSON(w, ImportResult{Records: recs, Warning: warn})
}

func (s *appServer) handleExportExcel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", 405)
		return
	}
	var req struct {
		Rows []Guest `json:"rows"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 10<<20)).Decode(&req); err != nil {
		writeErr(w, 400, "Dữ liệu xuất không hợp lệ")
		return
	}
	b, err := makeTemplateExcel(req.Rows)
	if err != nil {
		writeErr(w, 500, "Không tạo được Excel: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", "attachment; filename=XNC.xlsx")
	_, _ = w.Write(b)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
func writeErr(w http.ResponseWriter, status int, msg string) {
	w.WriteHeader(status)
	writeJSON(w, map[string]string{"error": msg})
}
func sanitizeName(s string) string {
	s = filepath.Base(s)
	s = regexp.MustCompile(`[<>:"/\\|?*]+`).ReplaceAllString(s, "_")
	if s == "" {
		s = "input"
	}
	return s
}

var aliases = map[string]string{
	"ho ten": "fullName", "họ tên": "fullName", "name": "fullName", "full name": "fullName", "guest name": "fullName", "passenger name": "fullName",
	"ngay sinh": "birthDate", "ngày sinh": "birthDate", "date of birth": "birthDate", "birth date": "birthDate", "dob": "birthDate", "d o b": "birthDate", "birthdate": "birthDate", "birthday": "birthDate",
	"ngay sinh dung den": "birthPrecision", "ngày sinh đúng đến": "birthPrecision", "birth precision": "birthPrecision", "precision": "birthPrecision",
	"gioi tinh": "gender", "giới tính": "gender", "gender": "gender", "sex": "gender",
	"quoc tich": "nationality", "quốc tịch": "nationality", "ma quoc tich": "nationality", "mã quốc tịch": "nationality", "nationality": "nationality", "country code": "nationality", "country": "nationality",
	"so ho chieu": "passport", "số hộ chiếu": "passport", "passport": "passport", "passport no": "passport", "passport number": "passport", "document no": "passport",
	"so phong": "room", "số phòng": "room", "room": "room", "room no": "room", "room number": "room",
	"ngay den": "arrival", "ngày đến": "arrival", "arrival": "arrival", "arrival date": "arrival", "check in": "arrival", "check-in": "arrival",
	"ngay di": "departure", "ngày đi": "departure", "ngay di du kien": "departure", "ngày đi dự kiến": "departure", "departure": "departure", "check out": "departure", "check-out": "departure",
	"ngay tra phong": "checkout", "ngày trả phòng": "checkout", "checkout date": "checkout", "actual departure": "checkout",
}

func readSpreadsheet(path string) ([]Guest, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".csv" {
		return readCSV(path)
	}
	if ext == ".xls" {
		tmp := strings.TrimSuffix(path, ext) + ".csv"
		if err := convertXlsToCSV(path, tmp); err != nil {
			return nil, fmt.Errorf("file .xls cần Microsoft Excel hoặc hãy lưu lại thành .xlsx: %w", err)
		}
		return readCSV(tmp)
	}
	return readXLSX(path)
}
func readCSV(path string) ([]Guest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	txt := decodeText(b)
	delim := ','
	first := strings.Split(txt, "\n")[0]
	for _, d := range []rune{';', '\t', '|'} {
		if strings.Count(first, string(d)) > strings.Count(first, string(delim)) {
			delim = d
		}
	}
	cr := csv.NewReader(strings.NewReader(txt))
	cr.Comma = delim
	cr.FieldsPerRecord = -1
	rows, err := cr.ReadAll()
	if err != nil {
		return nil, err
	}
	return recordsFromMatrix(rows), nil
}
func readXLSX(path string) ([]Guest, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	shared := []string{}
	if f := files["xl/sharedStrings.xml"]; f != nil {
		b, _ := readZipFile(f)
		shared = parseSharedStrings(b)
	}
	var names []string
	for n := range files {
		if strings.HasPrefix(n, "xl/worksheets/sheet") && strings.HasSuffix(n, ".xml") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	var best []Guest
	for _, n := range names {
		b, _ := readZipFile(files[n])
		rows := parseSheetXML(b, shared)
		recs := recordsFromMatrix(rows)
		if len(recs) > len(best) {
			best = recs
		}
	}
	if len(best) == 0 {
		return nil, errors.New("không tìm thấy bảng dữ liệu khách trong Excel")
	}
	return best, nil
}
func readZipFile(f *zip.File) ([]byte, error) {
	r, e := f.Open()
	if e != nil {
		return nil, e
	}
	defer r.Close()
	return io.ReadAll(r)
}
func parseSharedStrings(b []byte) []string {
	type T struct {
		Text string `xml:",chardata"`
	}
	type SI struct {
		Ts   []T `xml:"t"`
		Runs []struct {
			T T `xml:"t"`
		} `xml:"r"`
	}
	var root struct {
		SI []SI `xml:"si"`
	}
	_ = xml.Unmarshal(b, &root)
	out := make([]string, 0, len(root.SI))
	for _, si := range root.SI {
		s := ""
		for _, t := range si.Ts {
			s += t.Text
		}
		for _, r := range si.Runs {
			s += r.T.Text
		}
		out = append(out, s)
	}
	return out
}
func parseSheetXML(b []byte, shared []string) [][]string {
	type C struct {
		R  string `xml:"r,attr"`
		T  string `xml:"t,attr"`
		V  string `xml:"v"`
		IS struct {
			T string `xml:"t"`
		} `xml:"is"`
	}
	type R struct {
		C []C `xml:"c"`
	}
	var ws struct {
		Rows []R `xml:"sheetData>row"`
	}
	_ = xml.Unmarshal(b, &ws)
	var out [][]string
	for _, row := range ws.Rows {
		max := 0
		vals := map[int]string{}
		for _, c := range row.C {
			col := cellCol(c.R)
			if col > max {
				max = col
			}
			v := c.V
			if c.T == "s" {
				i, _ := strconv.Atoi(v)
				if i >= 0 && i < len(shared) {
					v = shared[i]
				}
			} else if c.T == "inlineStr" {
				v = c.IS.T
			}
			vals[col] = v
		}
		arr := make([]string, max+1)
		for i, v := range vals {
			arr[i] = v
		}
		out = append(out, arr)
	}
	return out
}
func cellCol(ref string) int {
	n := 0
	for _, r := range ref {
		if r < 'A' || r > 'Z' {
			break
		}
		n = n*26 + int(r-'A'+1)
	}
	return n - 1
}

func recordsFromMatrix(rows [][]string) []Guest {
	bestIdx, bestScore := -1, 0
	bestMap := map[string][]int{}
	limit := len(rows)
	if limit > 25 {
		limit = 25
	}
	for i := 0; i < limit; i++ {
		m := detectMatrixHeader(rows[i])
		score := len(m)
		// A usable guest table must contain a name plus at least one identifying
		// field. Other columns may legitimately be absent and can be completed in
		// the review grid before export.
		_, hasName := m["fullName"]
		hasIdentity := len(m["birthDate"]) > 0 || len(m["passport"]) > 0 || len(m["nationality"]) > 0 || len(m["room"]) > 0 || len(m["gender"]) > 0
		if !hasName || !hasIdentity {
			continue
		}
		if score > bestScore {
			bestIdx, bestScore, bestMap = i, score, m
		}
	}
	var out []Guest
	if bestIdx >= 0 && bestScore >= 2 {
		for _, r := range rows[bestIdx+1:] {
			get := func(f string) string {
				cols := bestMap[f]
				if len(cols) == 0 {
					return ""
				}
				var vals []string
				for _, j := range cols {
					if j < 0 || j >= len(r) {
						continue
					}
					v := strings.TrimSpace(r[j])
					if v != "" {
						vals = append(vals, v)
					}
				}
				if len(vals) == 0 {
					return ""
				}
				if f == "fullName" {
					return strings.Join(vals, " ")
				}
				return vals[0]
			}
			g := Guest{
				FullName:       get("fullName"),
				BirthDate:      get("birthDate"),
				BirthPrecision: get("birthPrecision"),
				Gender:         get("gender"),
				Nationality:    get("nationality"),
				Passport:       get("passport"),
				Room:           get("room"),
				Arrival:        get("arrival"),
				Departure:      get("departure"),
				Checkout:       get("checkout"),
			}
			if guestHasData(g) {
				out = append(out, g)
			}
		}
		return out
	}
	var lines []string
	for _, r := range rows {
		lines = append(lines, strings.Join(r, "   "))
	}
	return recordsFromLines(lines)
}

// detectMatrixHeader maps a header row to standard fields. Some tour files
// split the heading FULL NAME across two cells while the actual name occupies
// three columns (surname, first component, second component). In that layout,
// all columns from FULL up to the next recognized field belong to FullName.
func detectMatrixHeader(row []string) map[string][]int {
	m := map[string][]int{}
	norm := make([]string, len(row))
	for j, c := range row {
		norm[j] = normalizeKey(c)
		if f := aliases[norm[j]]; f != "" {
			if len(m[f]) == 0 {
				m[f] = []int{j}
			}
		}
	}

	for j := 0; j+1 < len(row); j++ {
		if norm[j] != "full" || norm[j+1] != "name" {
			continue
		}
		end := len(row)
		for k := j + 2; k < len(row); k++ {
			if f := aliases[norm[k]]; f != "" && f != "fullName" {
				end = k
				break
			}
		}
		cols := make([]int, 0, end-j)
		for k := j; k < end; k++ {
			cols = append(cols, k)
		}
		m["fullName"] = cols
		break
	}

	// Also support separate surname and given-name headings without requiring
	// callers to rename the spreadsheet manually.
	var surnameCols, givenCols []int
	for j, n := range norm {
		switch n {
		case "surname", "last name", "family name":
			surnameCols = append(surnameCols, j)
		case "given name", "given names", "first name", "middle name":
			givenCols = append(givenCols, j)
		}
	}
	if len(surnameCols)+len(givenCols) > 0 {
		m["fullName"] = append(surnameCols, givenCols...)
	}
	return m
}

func guestHasData(g Guest) bool {
	return strings.TrimSpace(g.FullName+g.BirthDate+g.Passport+g.Room+g.Nationality) != ""
}

func readWord(path, tempDir string) ([]Guest, string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	docx := path
	if ext == ".doc" {
		docx = filepath.Join(tempDir, "converted.docx")
		if err := convertDocToDocx(path, docx); err != nil {
			return nil, "", fmt.Errorf("file .doc cần Microsoft Word hoặc hãy lưu lại thành .docx: %w", err)
		}
	}
	rows, paras, images, err := parseDocx(docx, tempDir)
	if err != nil {
		return nil, "", err
	}
	recs := recordsFromWordRows(rows)
	if len(recs) == 0 {
		recs = recordsFromMatrix(rows)
	}
	if len(recs) == 0 {
		recs = recordsFromLines(paras)
	}
	for _, img := range images {
		txt, e := ocrImage(img, tempDir, false)
		if e == nil {
			recs = append(recs, recordsFromLines(strings.Split(txt, "\n"))...)
		}
	}
	warn := ""
	if len(recs) == 0 {
		warn = "Word đã mở được nhưng chưa nhận thấy dòng khách. Hãy kiểm tra bảng hoặc ảnh trong file."
	}
	return dedupeGuests(recs), warn, nil
}
func parseDocx(path, tempDir string) ([][]string, []string, []string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, nil, nil, err
	}
	defer zr.Close()
	var doc []byte
	var images []string
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			doc, _ = readZipFile(f)
		}
		if strings.HasPrefix(f.Name, "word/media/") {
			b, _ := readZipFile(f)
			p := filepath.Join(tempDir, filepath.Base(f.Name))
			_ = os.WriteFile(p, b, 0644)
			images = append(images, p)
		}
	}
	if len(doc) == 0 {
		return nil, nil, nil, errors.New("file Word không có document.xml")
	}
	rows := parseDocxTables(doc)
	paras := parseDocxParagraphs(doc)
	return rows, paras, images, nil
}

type docxTable struct {
	Rows []docxRow `xml:"tr"`
}
type docxRow struct {
	Cells []docxCell `xml:"tc"`
}
type docxCell struct {
	Text string
}

// UnmarshalXML collects all text inside a cell, including text stored in a
// nested table. Many Asian rooming lists use a small nested table inside the
// name cell (local-script name on the first line, Latin name on the second).
func (c *docxCell) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	depth := 1
	var parts []string
	for depth > 0 {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "t" {
				var text string
				if err := d.DecodeElement(&text, &t); err != nil {
					return err
				}
				if strings.TrimSpace(text) != "" {
					parts = append(parts, text)
				}
			} else {
				depth++
			}
		case xml.EndElement:
			depth--
		}
	}
	c.Text = strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
	return nil
}

func parseDocxTables(b []byte) [][]string {
	var doc struct {
		Body struct {
			Tables []docxTable `xml:"tbl"`
		} `xml:"body"`
	}
	if err := xml.Unmarshal(b, &doc); err != nil {
		return nil
	}
	var rows [][]string
	// Only direct tables in the document body are returned. Nested tables are
	// already folded into their parent cell by docxCell.UnmarshalXML, which
	// prevents one guest row from being broken into several one-cell rows.
	for _, table := range doc.Body.Tables {
		for _, r := range table.Rows {
			row := make([]string, 0, len(r.Cells))
			for _, c := range r.Cells {
				row = append(row, strings.TrimSpace(c.Text))
			}
			rows = append(rows, row)
		}
	}
	return rows
}

func recordsFromWordRows(rows [][]string) []Guest {
	header := -1
	roomCol, nameCol, birthCol, passportCol := -1, -1, -1, -1
	for i, row := range rows {
		for j, raw := range row {
			u := strings.ToUpper(strings.Join(strings.Fields(raw), ""))
			switch {
			case strings.Contains(u, "房號") || strings.Contains(u, "房号") || strings.Contains(u, "ROOM"):
				roomCol = j
			case strings.Contains(u, "姓名") || strings.Contains(u, "英文姓名") || strings.Contains(u, "NAME"):
				nameCol = j
			case strings.Contains(u, "生日") || strings.Contains(u, "出生日期") || strings.Contains(u, "DATEOFBIRTH") || u == "DOB":
				birthCol = j
			case strings.Contains(u, "護照") || strings.Contains(u, "护照") || strings.Contains(u, "PASSPORT"):
				passportCol = j
			}
		}
		if nameCol >= 0 && birthCol >= 0 && passportCol >= 0 {
			header = i
			break
		}
	}
	if header < 0 {
		return nil
	}
	cell := func(row []string, idx int) string {
		if idx < 0 || idx >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[idx])
	}
	currentRoom := ""
	var out []Guest
	for _, row := range rows[header+1:] {
		if raw := cell(row, roomCol); raw != "" {
			currentRoom = cleanWordRoom(raw)
		}
		nameRaw := cell(row, nameCol)
		m := titleRe.FindStringIndex(nameRaw)
		if m == nil {
			continue
		}
		title := strings.ToUpper(nameRaw[m[0]:m[1]])
		name := cleanName(nameRaw[m[1]:])
		if name == "" {
			continue
		}
		dob := plausibleBirthDate(cell(row, birthCol))
		passport := passportBeforeDate(cell(row, passportCol))
		gender := "M"
		if title != "MR" {
			gender = "F"
		}
		g := Guest{
			FullName:       name,
			BirthDate:      dob,
			BirthPrecision: precisionForDate(dob),
			Gender:         gender,
			Passport:       passport,
			Room:           currentRoom,
		}
		if guestHasData(g) {
			out = append(out, g)
		}
	}
	return dedupeGuests(out)
}

func cleanWordRoom(s string) string {
	u := strings.ToUpper(strings.Join(strings.Fields(s), " "))
	u = regexp.MustCompile(`(?i)\b(DBL|TWN|TWIN|TRPL|TRIPLE|DOUBLE|SGL|SINGLE|ROOM)\b`).ReplaceAllString(u, " ")
	u = strings.Join(strings.Fields(u), " ")
	return strings.TrimSpace(u)
}

func plausibleBirthDate(s string) string {
	nowYear := time.Now().Year()
	for _, idx := range dateRe.FindAllStringIndex(s, -1) {
		v := formatDateToken(s[idx[0]:idx[1]])
		parts := strings.Split(v, "/")
		if len(parts) != 3 {
			continue
		}
		y, err := strconv.Atoi(parts[2])
		if err == nil && y >= 1900 && y <= nowYear {
			return v
		}
	}
	return ""
}

func passportBeforeDate(s string) string {
	prefix := s
	if idx := dateRe.FindStringIndex(s); idx != nil {
		prefix = s[:idx[0]]
	}
	matches := regexp.MustCompile(`(?i)\b[A-Z0-9]{6,12}\b`).FindAllString(prefix, -1)
	for i := len(matches) - 1; i >= 0; i-- {
		q := strings.ToUpper(matches[i])
		if regexp.MustCompile(`\d`).MatchString(q) {
			return q
		}
	}
	return ""
}

func parseDocxParagraphs(b []byte) []string {
	dec := xml.NewDecoder(bytes.NewReader(b))
	var out []string
	var p strings.Builder
	inP := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "p" {
				inP = true
				p.Reset()
			} else if t.Name.Local == "t" && inP {
				var s string
				_ = dec.DecodeElement(&s, &t)
				if p.Len() > 0 {
					p.WriteByte(' ')
				}
				p.WriteString(s)
			}
		case xml.EndElement:
			if t.Name.Local == "p" && inP {
				s := strings.TrimSpace(p.String())
				if s != "" {
					out = append(out, s)
				}
				inP = false
			}
		}
	}
	return out
}

func readPDF(path, tempDir string) ([]Guest, string, error) {
	txtPath := filepath.Join(tempDir, "pdf.txt")
	// Word extracts real text/tables quickly when available; Windows PDF OCR is the offline fallback.
	if err := pdfTextWithWord(path, txtPath); err != nil || fileSize(txtPath) < 10 {
		if err := pdfOCRWindows(path, txtPath); err != nil {
			return nil, "", fmt.Errorf("không đọc được PDF: %w", err)
		}
	}
	b, err := os.ReadFile(txtPath)
	if err != nil {
		return nil, "", err
	}
	recs := recordsFromLines(strings.Split(decodeText(b), "\n"))
	warn := ""
	if len(recs) == 0 {
		warn = "PDF đã được đọc nhưng chưa nhận thấy dòng khách. Có thể cần ảnh rõ hơn hoặc bảng thẳng hơn."
	}
	return dedupeGuests(recs), warn, nil
}
func readTableImage(path, tempDir string) ([]Guest, string, error) {
	txt, err := ocrImage(path, tempDir, false)
	if err != nil {
		return nil, "", err
	}
	recs := recordsFromLines(strings.Split(txt, "\n"))
	warn := ""
	if len(recs) == 0 {
		warn = "Đã OCR ảnh nhưng chưa nhận thấy dòng khách hợp lệ."
	}
	return dedupeGuests(recs), warn, nil
}

func readPassport(path, tempDir string) ([]Guest, string, error) {
	txt, err := ocrPassport(path, tempDir)
	if err != nil {
		return nil, "", err
	}
	// Diagnostic shown to the user when results are imperfect: without Tesseract the
	// MRZ is read by Windows OCR, which mangles the machine-readable font.
	noTess := ""
	if tesseractPath() == "" {
		noTess = " [Máy chưa dùng được Tesseract — MRZ đang do Windows OCR đọc nên dễ sai; hãy cài Tesseract-OCR vào C:\\Program Files\\Tesseract-OCR rồi mở lại app.]"
	} else if _, _, ok := mrzOCR(); !ok {
		noTess = " [Để đọc MRZ chính xác hơn khi offline, hãy đặt file mrz.traineddata (hoặc ocrb.traineddata) vào thư mục tessdata của Tesseract rồi mở lại app.]"
	}
	if g, ok := parseMRZ(txt); ok {
		warn := ""
		// A degraded MRZ read can win on the checksum-protected fields (passport,
		// birth, nationality) yet lack the name (line-1 chevrons OCR'd as letters, or
		// a sparse/truncated line-1) or the sex (a truncated line-2 window). Fill any
		// missing field from the printed visual zone, which OCRs far more reliably.
		// Cross-check name and sex against the printed visual zone. The MRZ name can be
		// incomplete (a truncated line-1 leaves only the last given name, e.g. "HOANG"
		// instead of "TRAN DIEP THI HOANG") or garbled (chevrons OCR'd as letters). The
		// printed zone often carries the full name, so keep whichever candidate is more
		// complete (more name parts) and not garbled.
		v := parsePassportVisual(txt)
		g.FullName = preferCompleteName(g.FullName, v.FullName)
		if nameLooksGarbled(g.FullName) {
			g.FullName = "" // drop garbage rather than show it
		}
		if g.Gender == "" && v.Gender != "" {
			g.Gender = v.Gender
		}
		normalizeGuest(&g)
		if g.FullName == "" {
			warn = "Đã đọc được MRZ nhưng chưa lấy được họ tên; vui lòng nhập họ tên." + noTess
		}
		return []Guest{g}, warn, nil
	}
	g := parsePassportVisual(txt)
	if guestHasData(g) {
		return []Guest{g}, "MRZ chưa xác thực đầy đủ; vui lòng kiểm tra các ô được đọc từ vùng thông tin." + noTess, nil
	}
	return nil, "", errors.New("không đọc được MRZ hoặc vùng thông tin hộ chiếu; hãy dùng ảnh thẳng, đủ sáng và thấy rõ hai dòng MRZ" + noTess)
}

func recordsFromLines(lines []string) []Guest {
	var out []Guest
	for _, raw := range lines {
		line := cleanOCRLine(raw)
		if len(line) < 5 || isNoiseLine(line) {
			continue
		}
		if g, ok := parseRoomingLine(line); ok {
			out = append(out, g)
			continue
		}
		if g, ok := parseGenericLine(line); ok {
			out = append(out, g)
		}
	}
	return dedupeGuests(out)
}
func isNoiseLine(s string) bool {
	u := strings.ToUpper(s)
	noise := []string{"TPE/HAN", "HAN/TPE", "TOTAL", "PAX", "TWN", "DBL", "TRPL", "TOUR/", "FLIGHT", "BR385", "BR386"}
	for _, n := range noise {
		if strings.Contains(u, n) {
			return true
		}
	}
	return false
}

var dateRe = regexp.MustCompile(`(?i)\b(?:\d{1,2}[./-]\d{1,2}[./-](?:\d{2}|\d{4})|\d{4}[./-]\d{1,2}[./-]\d{1,2}|\d{1,2}[./-]\d{4}|(?:19|20)\d{2})\b`)
var titleRe = regexp.MustCompile(`(?i)\b(MR|MS|MRS|MISS)\b`)
var passRe = regexp.MustCompile(`\b[A-Z0-9]{6,12}\b`)

func parseRoomingLine(line string) (Guest, bool) {
	m := titleRe.FindStringIndex(line)
	if m == nil {
		return Guest{}, false
	}
	after := strings.TrimSpace(line[m[1]:])
	dates := dateRe.FindAllStringIndex(after, -1)
	if len(dates) < 2 {
		return Guest{}, false
	}
	prefix := strings.TrimSpace(after[:dates[0][0]])
	tokens := strings.Fields(prefix)
	pi := -1
	for i := len(tokens) - 1; i >= 0; i-- {
		t := strings.Trim(tokens[i], ".,;:()[]")
		if len(t) >= 6 && len(t) <= 12 && regexp.MustCompile(`\d`).MatchString(t) && regexp.MustCompile(`^[A-Za-z0-9]+$`).MatchString(t) {
			pi = i
			break
		}
	}
	if pi < 0 {
		return Guest{}, false
	}
	passport := strings.ToUpper(strings.Trim(tokens[pi], ".,;:()[]"))
	name := strings.Join(tokens[:pi], " ")
	name = cleanName(name)
	if name == "" {
		return Guest{}, false
	}
	dob := formatDateToken(after[dates[len(dates)-1][0]:dates[len(dates)-1][1]])
	gender := "M"
	title := strings.ToUpper(line[m[0]:m[1]])
	if title != "MR" {
		gender = "F"
	}
	return Guest{FullName: name, BirthDate: dob, BirthPrecision: precisionForDate(dob), Gender: gender, Passport: passport}, true
}
func parseGenericLine(line string) (Guest, bool) {
	matches := dateRe.FindAllStringIndex(line, -1)
	if len(matches) == 0 {
		return Guest{}, false
	}
	before := strings.TrimSpace(line[:matches[0][0]])
	before = regexp.MustCompile(`^\s*\d{1,4}[.)-]?\s+`).ReplaceAllString(before, "")
	if !regexp.MustCompile(`[A-Za-z]`).MatchString(before) {
		return Guest{}, false
	}
	g := Guest{FullName: cleanName(before), BirthDate: formatDateToken(line[matches[0][0]:matches[0][1]])}
	g.BirthPrecision = precisionForDate(g.BirthDate)
	rest := line[matches[0][1]:]
	if m := titleRe.FindString(rest); m != "" {
		if strings.EqualFold(m, "MR") {
			g.Gender = "M"
		} else {
			g.Gender = "F"
		}
	}
	for _, t := range strings.Fields(rest) {
		q := strings.ToUpper(strings.Trim(t, ".,;:()[]"))
		if g.Nationality == "" && regexp.MustCompile(`^[A-Z]{3}$`).MatchString(q) {
			g.Nationality = q
			continue
		}
		if g.Passport == "" && len(q) >= 6 && len(q) <= 12 && regexp.MustCompile(`\d`).MatchString(q) && regexp.MustCompile(`^[A-Z0-9]+$`).MatchString(q) {
			g.Passport = q
		}
	}
	if len(matches) > 1 {
		g.Arrival = formatDateToken(line[matches[1][0]:matches[1][1]])
	}
	if len(matches) > 2 {
		g.Departure = formatDateToken(line[matches[2][0]:matches[2][1]])
	}
	if g.FullName == "" {
		return Guest{}, false
	}
	return g, true
}
func cleanOCRLine(s string) string {
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(s)
}
// nameLooksGarbled flags names produced when MRZ chevron fillers ("<") were OCR'd
// as letters, e.g. "PHAMK KAMY VI KKKKKRRRR". Such names contain a long run of one
// repeated letter or are implausibly long — patterns real names essentially never
// have. Used to decide whether to fall back to the printed visual-zone name.
// (Go's RE2 has no backreferences, so the repeat run is scanned manually.)
func nameLooksGarbled(name string) bool {
	s := strings.ToUpper(strings.ReplaceAll(name, " ", ""))
	if s == "" {
		return false
	}
	if len(s) > 40 {
		return true
	}
	run := 1
	for i := 1; i < len(s); i++ {
		if s[i] == s[i-1] {
			run++
			if run >= 4 {
				return true
			}
		} else {
			run = 1
		}
	}
	return false
}

// preferCompleteName picks the more complete of two candidate names. A non-garbled
// candidate with more word tokens wins; if only one is usable it is returned; ties
// keep the primary (MRZ) name. This recovers full names when the MRZ line-1 read is
// truncated to a single token but the printed visual zone has the whole name.
func preferCompleteName(primary, alt string) string {
	p := strings.TrimSpace(primary)
	a := strings.TrimSpace(alt)
	pGood := p != "" && !nameLooksGarbled(p)
	aGood := a != "" && !nameLooksGarbled(a)
	switch {
	case pGood && aGood:
		if len(strings.Fields(a)) > len(strings.Fields(p)) {
			return a
		}
		return p
	case aGood:
		return a
	default:
		return p // primary (may be garbled/empty; caller drops garbage)
	}
}

func cleanName(s string) string {
	u := strings.ToUpper(s)
	u = regexp.MustCompile(`(?i)\b(MR|MS|MRS|MISS|T/L|DBL|TWN|TRPL|FES|FEF)\b`).ReplaceAllString(u, " ")
	u = regexp.MustCompile(`\b[A-Z0-9]*\d[A-Z0-9]{5,}\b`).ReplaceAllString(u, " ")
	u = strings.Join(strings.Fields(u), " ")
	return strings.Trim(u, " /,.-")
}
func dedupeGuests(in []Guest) []Guest {
	seen := map[string]bool{}
	var out []Guest
	for _, g := range in {
		normalizeGuest(&g)
		key := normalizeKey(g.Passport)
		if key == "" {
			key = normalizeKey(g.FullName + "|" + g.BirthDate)
		}
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, g)
	}
	return out
}

func normalizeGuest(g *Guest) {
	g.FullName = cleanName(g.FullName)
	g.Passport = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(g.Passport), " ", ""))
	g.Nationality = strings.ToUpper(strings.TrimSpace(g.Nationality))
	g.Gender = normalizeGender(g.Gender)
	g.BirthDate = formatDateToken(g.BirthDate)
	g.Arrival = formatDateToken(g.Arrival)
	g.Departure = formatDateToken(g.Departure)
	g.Checkout = formatDateToken(g.Checkout)
	if g.BirthPrecision == "" {
		g.BirthPrecision = precisionForDate(g.BirthDate)
	} else {
		p := strings.ToUpper(strings.TrimSpace(g.BirthPrecision))
		if strings.HasPrefix(p, "D") {
			g.BirthPrecision = "D"
		} else if strings.HasPrefix(p, "M") {
			g.BirthPrecision = "M"
		} else if strings.HasPrefix(p, "Y") {
			g.BirthPrecision = "Y"
		}
	}
}
func normalizeGender(s string) string {
	u := normalizeKey(s)
	if u == "m" || strings.Contains(u, "nam") || u == "male" || u == "mr" {
		return "M"
	}
	if u == "f" || strings.Contains(u, "nu") || u == "female" || u == "ms" || u == "mrs" {
		return "F"
	}
	return ""
}
func formatDateToken(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Excel stores dates as serial numbers. Convert only plausible date serials.
	if f, err := strconv.ParseFloat(s, 64); err == nil && f >= 20000 && f <= 80000 {
		d := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(f))
		return d.Format("02/01/2006")
	}
	s = strings.ReplaceAll(strings.ReplaceAll(s, ".", "/"), "-", "/")
	parts := strings.Split(s, "/")
	if len(parts) == 3 {
		if len(parts[0]) == 4 {
			parts[0], parts[2] = parts[2], parts[0]
		}
		if len(parts[2]) == 2 {
			y, _ := strconv.Atoi(parts[2])
			if y <= 30 {
				parts[2] = fmt.Sprintf("20%02d", y)
			} else {
				parts[2] = fmt.Sprintf("19%02d", y)
			}
		}
		d, _ := strconv.Atoi(parts[0])
		m, _ := strconv.Atoi(parts[1])
		y, _ := strconv.Atoi(parts[2])
		if d > 0 && m > 0 && y > 0 {
			return fmt.Sprintf("%02d/%02d/%04d", d, m, y)
		}
	}
	if len(parts) == 2 {
		m, _ := strconv.Atoi(parts[0])
		y, _ := strconv.Atoi(parts[1])
		if m > 0 && m <= 12 && y > 0 {
			return fmt.Sprintf("%02d/%04d", m, y)
		}
	}
	if regexp.MustCompile(`^\d{4}$`).MatchString(s) {
		return s
	}
	return strings.TrimSpace(s)
}
func precisionForDate(s string) string {
	if regexp.MustCompile(`^\d{2}/\d{2}/\d{4}$`).MatchString(s) {
		return "D"
	}
	if regexp.MustCompile(`^\d{2}/\d{4}$`).MatchString(s) {
		return "M"
	}
	if regexp.MustCompile(`^\d{4}$`).MatchString(s) {
		return "Y"
	}
	return ""
}
func normalizeKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
func decodeText(b []byte) string {
	if len(b) >= 2 && b[0] == 0xff && b[1] == 0xfe {
		r := make([]rune, 0, (len(b)-2)/2)
		for i := 2; i+1 < len(b); i += 2 {
			r = append(r, rune(uint16(b[i])|uint16(b[i+1])<<8))
		}
		return string(r)
	}
	return strings.TrimPrefix(string(b), "\ufeff")
}
func fileSize(p string) int64 {
	st, e := os.Stat(p)
	if e != nil {
		return 0
	}
	return st.Size()
}

func findTesseract() string {
	cands := []string{}
	if p := os.Getenv("ProgramFiles"); p != "" {
		cands = append(cands, filepath.Join(p, "Tesseract-OCR", "tesseract.exe"))
	}
	if p := os.Getenv("ProgramFiles(x86)"); p != "" {
		cands = append(cands, filepath.Join(p, "Tesseract-OCR", "tesseract.exe"))
	}
	if p := os.Getenv("LOCALAPPDATA"); p != "" {
		cands = append(cands, filepath.Join(p, "Programs", "Tesseract-OCR", "tesseract.exe"))
	}
	if p, e := exec.LookPath("tesseract.exe"); e == nil {
		cands = append(cands, p)
	}
	for _, p := range cands {
		if _, e := os.Stat(p); e == nil {
			return p
		}
	}
	return ""
}

// The Tesseract lookup touches the filesystem, so cache it. During passport OCR
// this function is now called from several goroutines at once.
var (
	tessOnce sync.Once
	tessExe  string
	procSeq  atomic.Int64
)

func tesseractPath() string {
	// Only cache a positive result. If Tesseract is installed while the app is
	// already running, the next lookup still finds it (previously a first empty
	// result was cached for the whole session, so a mid-session install was
	// ignored until restart).
	if p := tessExe; p != "" {
		return p
	}
	tessMu.Lock()
	defer tessMu.Unlock()
	if tessExe == "" {
		tessExe = findTesseract()
	}
	return tessExe
}

var tessMu sync.Mutex

// mrzOCR reports the Tesseract language to use for the machine-readable zone. It
// prefers a dedicated OCR-B model (mrz.traineddata / ocrb.traineddata) dropped
// into any tessdata folder we know about; when none is present it returns
// ("eng","",false) and the caller keeps the previous generic-English behavior.
// Only a positive result is cached, so dropping the model in mid-session is
// picked up on the next passport read without restarting.
var (
	mrzMu        sync.Mutex
	mrzLangCache string
	mrzDirCache  string
	mrzHave      bool
)

func mrzOCR() (string, string, bool) {
	mrzMu.Lock()
	defer mrzMu.Unlock()
	if mrzHave {
		return mrzLangCache, mrzDirCache, true
	}
	l, d, ok := findMRZLangIn(mrzTessdataDirs())
	if ok {
		mrzLangCache, mrzDirCache, mrzHave = l, d, true
	}
	return l, d, ok
}

// mrzTessdataDirs lists the tessdata folders to search for an MRZ model, in
// priority order. %LOCALAPPDATA%\XNC Ocean\tessdata lets a user add the model
// without write access to Program Files.
func mrzTessdataDirs() []string {
	var dirs []string
	if p := os.Getenv("TESSDATA_PREFIX"); p != "" {
		dirs = append(dirs, p, filepath.Join(p, "tessdata"))
	}
	if tess := tesseractPath(); tess != "" {
		dirs = append(dirs, filepath.Join(filepath.Dir(tess), "tessdata"))
	}
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		dirs = append(dirs, filepath.Join(la, "XNC Ocean", "tessdata"))
	}
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Join(filepath.Dir(exe), "tessdata"))
	}
	return dirs
}

// findMRZLangIn returns the first MRZ/OCR-B model found among dirs as
// (langName, tessdataDir, true). The langName matches the traineddata basename
// so it can be passed straight to Tesseract's -l flag.
func findMRZLangIn(dirs []string) (string, string, bool) {
	seen := map[string]bool{}
	for _, d := range dirs {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		for _, name := range []string{"mrz", "MRZ", "ocrb", "OCRB", "OCR-B", "ocrb_int"} {
			if _, err := os.Stat(filepath.Join(d, name+".traineddata")); err == nil {
				return name, d, true
			}
		}
	}
	return "eng", "", false
}

// grayBand extracts rect from src as a contrast-stretched grayscale image. Working
// in Go removes the dependency on the PowerShell/System.Drawing preprocessing that
// silently fails on some machines and starves Tesseract of a clean MRZ crop.
func grayBand(src image.Image, rect image.Rectangle) *image.Gray {
	w, h := rect.Dx(), rect.Dy()
	g := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, gg, bb, _ := src.At(rect.Min.X+x, rect.Min.Y+y).RGBA()
			lum := int((299*(r>>8) + 587*(gg>>8) + 114*(bb>>8)) / 1000)
			v := (lum-128)*3/2 + 128
			if v < 0 {
				v = 0
			} else if v > 255 {
				v = 255
			}
			g.SetGray(x, y, color.Gray{Y: uint8(v)})
		}
	}
	return g
}

// otsuThreshold picks the grey level that best separates ink from paper.
func otsuThreshold(g *image.Gray) uint8 {
	var hist [256]int
	b := g.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			hist[g.GrayAt(x, y).Y]++
		}
	}
	total := b.Dx() * b.Dy()
	if total == 0 {
		return 128
	}
	sum := 0.0
	for i := 0; i < 256; i++ {
		sum += float64(i * hist[i])
	}
	sumB, wB, varMax, thr := 0.0, 0, 0.0, 128
	for i := 0; i < 256; i++ {
		wB += hist[i]
		if wB == 0 {
			continue
		}
		wF := total - wB
		if wF == 0 {
			break
		}
		sumB += float64(i * hist[i])
		mB := sumB / float64(wB)
		mF := (sum - sumB) / float64(wF)
		v := float64(wB) * float64(wF) * (mB - mF) * (mB - mF)
		if v > varMax {
			varMax = v
			thr = i
		}
	}
	return uint8(thr)
}

// sharpenGray applies an unsharp mask (original + (original - 3x3 box blur)) so
// slightly out-of-focus MRZ strokes get crisper edges before binarization. Edges
// are clamped. This raises the chance a mildly blurry crop reads cleanly enough to
// pass the MRZ checksum; it cannot recover heavily smeared photos.
func sharpenGray(src *image.Gray) *image.Gray {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewGray(image.Rect(0, 0, w, h))
	at := func(x, y int) int {
		if x < 0 {
			x = 0
		} else if x >= w {
			x = w - 1
		}
		if y < 0 {
			y = 0
		} else if y >= h {
			y = h - 1
		}
		return int(src.GrayAt(b.Min.X+x, b.Min.Y+y).Y)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sum := 0
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					sum += at(x+dx, y+dy)
				}
			}
			orig := at(x, y)
			v := orig + (orig - sum/9) // amount = 1.0
			if v < 0 {
				v = 0
			} else if v > 255 {
				v = 255
			}
			dst.SetGray(x, y, color.Gray{Y: uint8(v)})
		}
	}
	return dst
}

// binarize converts to pure black/white using the Otsu threshold. A clean binary
// image is what OCR-B recognition benefits from most on real, uneven photos.
func binarize(g *image.Gray) *image.Gray {
	t := otsuThreshold(g)
	b := g.Bounds()
	o := image.NewGray(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if g.GrayAt(x, y).Y > t {
				o.SetGray(x, y, color.Gray{Y: 255})
			} else {
				o.SetGray(x, y, color.Gray{Y: 0})
			}
		}
	}
	return o
}

func upscale2x(g *image.Gray) *image.Gray {
	b := g.Bounds()
	w, h := b.Dx(), b.Dy()
	o := image.NewGray(image.Rect(0, 0, w*2, h*2))
	for y := 0; y < h*2; y++ {
		for x := 0; x < w*2; x++ {
			o.SetGray(x, y, g.GrayAt(b.Min.X+x/2, b.Min.Y+y/2))
		}
	}
	return o
}

// cropMRZBandsGo decodes the photo and writes several bottom-of-page grayscale
// bands where the two MRZ lines live, plus a whole-image grayscale. Pure Go, so it
// works even when the PowerShell preprocessing is unavailable. Returns nil for
// formats Go can't decode (e.g. HEIC), in which case the caller keeps using the
// PowerShell crops / original image.
func cropMRZBandsGo(path, dir string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		log.Printf("go crop decode %s: %v", filepath.Base(path), err)
		return nil
	}
	b := img.Bounds()
	W, H := b.Dx(), b.Dy()
	if W < 60 || H < 60 {
		return nil
	}
	bands := [][2]float64{{0.60, 1.00}, {0.68, 1.00}, {0.50, 0.84}, {0.74, 1.00}}
	var out []string
	writeImg := func(g *image.Gray, name string) {
		if g.Bounds().Dx() < 1500 {
			g = upscale2x(g)
		}
		p := filepath.Join(dir, name)
		wf, e := os.Create(p)
		if e != nil {
			return
		}
		if png.Encode(wf, g) == nil {
			out = append(out, p)
		}
		wf.Close()
	}
	// For each MRZ band emit a contrast-stretched grayscale, an Otsu binary, and a
	// sharpened-then-binarized variant. Whichever reads cleanest wins the checksum,
	// so all are OCR'd; the sharpened variant helps on mildly out-of-focus photos.
	for i, bd := range bands {
		y0 := b.Min.Y + int(float64(H)*bd[0])
		y1 := b.Min.Y + int(float64(H)*bd[1])
		if y1-y0 < 20 {
			continue
		}
		g := grayBand(img, image.Rect(b.Min.X, y0, b.Max.X, y1))
		writeImg(g, fmt.Sprintf("goband_%d.png", i))
		writeImg(binarize(g), fmt.Sprintf("goband_%d_bw.png", i))
		writeImg(binarize(sharpenGray(g)), fmt.Sprintf("goband_%d_sharp_bw.png", i))
	}
	full := grayBand(img, b)
	writeImg(full, "goband_full.png")
	writeImg(binarize(full), "goband_full_bw.png")
	writeImg(binarize(sharpenGray(full)), "goband_full_sharp_bw.png")
	return out
}

// runTesseract runs one Tesseract pass and returns the recognized text ("" on
// failure). psm is the page-segmentation mode.
func runTesseract(tess, path, tempDir, psm string, mrz bool) string {
	base := filepath.Join(tempDir, fmt.Sprintf("ocr_%d_%s", procSeq.Add(1), psm))
	// --oem 1 (LSTM) and an explicit --dpi avoid the slower legacy path and the
	// per-run resolution guess. For MRZ crops we also drop the dictionaries and the
	// inverted retry pass: the MRZ is a fixed OCR-B code.
	lang, oem := "eng", "1"
	args := []string{path, base}
	if mrz {
		// The machine-readable zone is OCR-B, a font the generic English model reads
		// poorly. When a dedicated MRZ/OCR-B model is installed (mrz.traineddata or
		// ocrb.traineddata in a tessdata folder), use it — this is what lets the MRZ
		// read accurately fully offline, the way a real passport reader does. The
		// model may be legacy-trained, so let Tesseract auto-pick the engine (--oem).
		if l, dir, ok := mrzOCR(); ok {
			lang = l
			oem = ""
			if dir != "" {
				args = append(args, "--tessdata-dir", dir)
			}
		}
	}
	args = append(args, "-l", lang, "--psm", psm, "--dpi", "300")
	if oem != "" {
		args = append(args, "--oem", oem)
	}
	if mrz {
		args = append(args,
			"-c", "tessedit_char_whitelist=ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789<",
			"-c", "load_system_dawg=0",
			"-c", "load_freq_dawg=0",
			"-c", "tessedit_do_invert=0",
		)
	}
	cmd := exec.Command(tess, args...)
	cmd.SysProcAttr = hiddenProcAttr()
	// One thread per process so the crops parallelize cleanly at the process level.
	cmd.Env = append(os.Environ(), "OMP_THREAD_LIMIT=1")
	if out, err := cmd.CombinedOutput(); err == nil {
		if b, e := os.ReadFile(base + ".txt"); e == nil {
			return decodeText(b)
		}
	} else {
		log.Printf("tesseract psm%s: %v %s", psm, err, string(out))
	}
	return ""
}

func ocrImage(path, tempDir string, mrz bool) (string, error) {
	seq := procSeq.Add(1)
	if tess := tesseractPath(); tess != "" {
		if mrz {
			// Two passes: psm 6 keeps the two MRZ lines intact, psm 11 (sparse) is
			// far better at the passport-number digits that psm 6 drops when there is
			// blank space around the strip. Feeding BOTH to the parser lets a
			// checksum-valid window emerge from whichever read the field correctly.
			var sb strings.Builder
			for _, psm := range []string{"6", "11"} {
				if t := runTesseract(tess, path, tempDir, psm, true); t != "" {
					sb.WriteString("\n")
					sb.WriteString(t)
				}
			}
			if sb.Len() > 0 {
				return sb.String(), nil
			}
		} else if t := runTesseract(tess, path, tempDir, "6", false); t != "" {
			return t, nil
		}
	}
	out := filepath.Join(tempDir, "winocr_"+strconv.FormatInt(seq, 10)+".txt")
	if err := runPowerShell(imageOCRScript, []string{"-InputPath", path, "-OutputPath", out}, tempDir); err != nil {
		return "", fmt.Errorf("máy chưa có Tesseract và Windows OCR lỗi: %w", err)
	}
	b, err := os.ReadFile(out)
	return decodeText(b), err
}

// ocrParallel OCRs the given files concurrently, capped at NumCPU workers, and
// returns the recognized text in the same order as the input files.
func ocrParallel(files []string, tempDir string, mrz bool) []string {
	out := make([]string, len(files))
	if len(files) == 0 {
		return out
	}
	workers := runtime.NumCPU()
	if workers < 1 {
		workers = 1
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, f := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, f string) {
			defer wg.Done()
			defer func() { <-sem }()
			if txt, err := ocrImage(f, tempDir, mrz); err == nil {
				out[i] = txt
			}
		}(i, f)
	}
	wg.Wait()
	return out
}
func passportDebugDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		return filepath.Join(os.TempDir(), "XNC Ocean", "last_passport")
	}
	return filepath.Join(base, "XNC Ocean", "last_passport")
}

func copyFileTo(src, dst string) {
	if b, e := os.ReadFile(src); e == nil {
		_ = os.WriteFile(dst, b, 0644)
	}
}

func ocrPassport(path, tempDir string) (string, error) {
	prep := filepath.Join(tempDir, "passport")
	_ = os.MkdirAll(prep, 0755)
	listPath := filepath.Join(prep, "list.txt")
	if err := runPowerShell(passportPrepScript, []string{"-InputPath", path, "-OutputDir", prep, "-ListPath", listPath}, tempDir); err != nil {
		log.Printf("passport prep: %v", err)
	}
	var crops []string
	if b, e := os.ReadFile(listPath); e == nil {
		for _, l := range strings.Split(decodeText(b), "\n") {
			l = strings.TrimSpace(l)
			if l != "" {
				crops = append(crops, l)
			}
		}
	}
	// Diagnostic folder: keep the actual MRZ crops (and, on failure, the raw OCR
	// text) so a user can send them when a passport still reads wrong. Persisted
	// outside the temp dir that gets cleaned up after each import.
	debugDir := passportDebugDir()
	_ = os.RemoveAll(debugDir)
	_ = os.MkdirAll(debugDir, 0755)

	// Go-native MRZ bands, produced without PowerShell. These are tried FIRST so the
	// MRZ still reaches Tesseract even when the PowerShell preprocessing produced no
	// usable crop (a common cause of the "MRZ never reads" failure).
	goBands := cropMRZBandsGo(path, debugDir)
	// Keep copies of the PowerShell MRZ crops for inspection too.
	for _, f := range crops {
		if strings.Contains(strings.ToLower(filepath.Base(f)), "mrz") {
			copyFileTo(f, filepath.Join(debugDir, filepath.Base(f)))
		}
	}
	log.Printf("passport crops: powershell=%d go=%d tesseract=%q", len(crops), len(goBands), tesseractPath())

	// MRZ candidates: Go bands and the targeted PowerShell bands first, the full
	// frame last as a fallback. OCR runs concurrently and stops at the first
	// checksum-valid MRZ.
	var mrzFiles []string
	mrzFiles = append(mrzFiles, goBands...)
	for _, f := range crops {
		if strings.Contains(strings.ToLower(filepath.Base(f)), "mrz") {
			mrzFiles = append(mrzFiles, f)
		}
	}
	mrzFiles = append(mrzFiles, path)

	var all strings.Builder
	workers := runtime.NumCPU()
	if workers < 1 {
		workers = 1
	}
	// Process MRZ candidates in priority-ordered batches. Each batch is OCR'd in
	// parallel; accuracy is preserved because acceptance still requires
	// mrzChecksReasonable + a complete guest, so a wrong crop can never win.
	for i := 0; i < len(mrzFiles); i += workers {
		end := i + workers
		if end > len(mrzFiles) {
			end = len(mrzFiles)
		}
		for _, txt := range ocrParallel(mrzFiles[i:end], tempDir, true) {
			if strings.TrimSpace(txt) == "" {
				continue
			}
			all.WriteString("\n")
			all.WriteString(txt)
			// Return early only when the MRZ already yields a COMPLETE guest. If the
			// winning window is a degraded/sparse read missing the sex or name, keep
			// going and let the visual zones (OCR'd below) fill those in.
			if g, ok := parseMRZ(all.String()); ok && g.FullName != "" && g.Gender != "" {
				return all.String(), nil
			}
		}
	}

	// MRZ did not validate. Record exactly what Tesseract read from the MRZ crops so
	// the cause (garbled OCR vs. parser gap) is visible in the log / debug folder.
	mrzOCR := all.String()
	log.Printf("passport MRZ not validated. raw MRZ OCR:\n%s", mrzOCR)
	_ = os.WriteFile(filepath.Join(debugDir, "ocr_mrz.txt"), []byte(mrzOCR), 0644)

	// MRZ was not conclusive: OCR the visual zones (DOB, sex, name, passport
	// number) concurrently as a cross-check, matching the original coverage.
	var visFiles []string
	for _, f := range crops {
		base := strings.ToLower(filepath.Base(f))
		if strings.Contains(base, "info") || strings.Contains(base, "passno") || strings.Contains(base, "document") {
			visFiles = append(visFiles, f)
		}
	}
	visFiles = append(visFiles, path)
	for i := 0; i < len(visFiles); i += workers {
		end := i + workers
		if end > len(visFiles) {
			end = len(visFiles)
		}
		for _, txt := range ocrParallel(visFiles[i:end], tempDir, false) {
			if strings.TrimSpace(txt) != "" {
				all.WriteString("\n")
				all.WriteString(txt)
			}
		}
	}
	if all.Len() == 0 {
		return "", errors.New("OCR hộ chiếu không khả dụng")
	}
	return all.String(), nil
}

func parseMRZ(text string) (Guest, bool) {
	cleanLines := mrzCleanLines(text)

	// Every checksum-valid TD3 second line found anywhere in the OCR text. The
	// second line carries passport number, nationality, birth date and sex, each
	// protected by a check digit, so a valid one is trustworthy on its own.
	var validL2 []string
	for _, raw := range cleanLines {
		for _, l2 := range mrzCandidateWindows(raw) {
			l2 = repairMRZNumericPositions(l2)
			if mrzChecksReasonable(l2) {
				validL2 = append(validL2, l2)
			}
		}
	}
	if len(validL2) == 0 {
		return Guest{}, false
	}

	// Tier 1: pair a valid second line with a first line (P<...) to also get the
	// name. This is the normal, complete result.
	for _, l1raw := range cleanLines {
		p := strings.Index(l1raw, "P<")
		if p < 0 {
			continue
		}
		l1 := l1raw[p:]
		if len(l1) < 10 { // "P<" + country + at least one name letter
			continue
		}
		if len(l1) < 44 {
			l1 += strings.Repeat("<", 44-len(l1))
		}
		l1 = l1[:44]
		for _, l2 := range validL2 {
			g := guestFromMRZLines(l1, l2)
			if guestHasData(g) && g.BirthDate != "" && g.Passport != "" && g.FullName != "" {
				return g, true
			}
		}
	}

	// Tier 2: the first line was not recognized (glare, crop, OCR dropped "P<"),
	// but a valid second line still yields passport, nationality, birth and sex.
	// The name is filled from the visual zone by the caller when this happens.
	l2 := validL2[0]
	g := guestFromMRZLines("P<"+strings.Repeat("<", 42), l2)
	if g.BirthDate != "" && g.Passport != "" {
		return g, true
	}
	return Guest{}, false
}

func mrzCleanLines(text string) []string {
	var out []string
	for _, raw := range strings.Split(strings.ToUpper(text), "\n") {
		l := regexp.MustCompile(`[^A-Z0-9<]`).ReplaceAllString(raw, "")
		if len(l) >= 20 {
			out = append(out, l)
		}
	}
	return out
}

var mrzDigitRe = regexp.MustCompile(`\d`)

func mrzCandidateWindows(raw string) []string {
	var out []string
	// The second line must reach at least the birth check digit (index 19) for the
	// checks to run. Phone-photo OCR frequently drops the trailing "<" fillers, so
	// pad shorter lines up to 44 instead of discarding them.
	if len(raw) < 20 {
		return out
	}
	s := raw
	if len(s) < 44 {
		s += strings.Repeat("<", 44-len(s))
	}
	for i := 0; i+44 <= len(s); i++ {
		w := s[i : i+44]
		// A TD3 second line normally has a digit check at index 9 and a sex at 20.
		if !mrzDigitRe.MatchString(w[:12]) {
			continue
		}
		out = append(out, w)
	}
	return out
}

func repairMRZNumericPositions(s string) string {
	b := []byte(s)
	for _, rng := range [][2]int{{9, 10}, {13, 20}, {21, 28}, {42, 44}} {
		for i := rng[0]; i < rng[1] && i < len(b); i++ {
			b[i] = mrzDigit(b[i])
		}
	}
	// Nationality should be alphabetic. OCR commonly confuses 1/I and 0/O.
	for i := 10; i < 13 && i < len(b); i++ {
		if b[i] == '1' {
			b[i] = 'I'
		}
		if b[i] == '0' {
			b[i] = 'O'
		}
	}
	// Try common OCR fixes in the passport number only when the check digit fails.
	if len(b) >= 10 && !mrzCheck(string(b[:9]), b[9]) {
		amb := map[byte][]byte{'O': {'O', '0'}, '0': {'0', 'O'}, 'I': {'I', '1'}, '1': {'1', 'I'}, 'L': {'L', '1'}, 'B': {'B', '8'}, '8': {'8', 'B'}, 'S': {'S', '5'}, '5': {'5', 'S'}, 'Z': {'Z', '2'}, '2': {'2', 'Z'}, 'G': {'G', '6'}, '6': {'6', 'G'}}
		var dfs func(int) bool
		dfs = func(pos int) bool {
			if pos == 9 {
				return mrzCheck(string(b[:9]), b[9])
			}
			orig := b[pos]
			opts := amb[orig]
			if len(opts) == 0 {
				opts = []byte{orig}
			}
			for _, v := range opts {
				b[pos] = v
				if dfs(pos + 1) {
					return true
				}
			}
			b[pos] = orig
			return false
		}
		_ = dfs(0)
	}
	return string(b)
}

func mrzDigit(c byte) byte {
	switch c {
	case 'O', 'Q', 'D':
		return '0'
	case 'I', 'L':
		return '1'
	case 'Z':
		return '2'
	case 'S':
		return '5'
	case 'G':
		return '6'
	case 'B':
		return '8'
	}
	return c
}

func guestFromMRZLines(l1, l2 string) Guest {
	names := strings.Split(l1[5:], "<<")
	surname := strings.ReplaceAll(names[0], "<", " ")
	given := ""
	if len(names) > 1 {
		given = strings.ReplaceAll(strings.Join(names[1:], " "), "<", " ")
	}
	g := Guest{FullName: cleanName(surname + " " + given), BirthDate: mrzDate(l2[13:19], true), BirthPrecision: "D", Gender: normalizeGender(string(l2[20])), Nationality: strings.ReplaceAll(l2[10:13], "<", ""), Passport: strings.ReplaceAll(l2[0:9], "<", "")}
	normalizeGuest(&g)
	return g
}

func mrzChecksReasonable(l2 string) bool {
	if len(l2) < 21 {
		return false
	}
	// Require the two check digits guarding the fields the app actually uses: the
	// passport number and the birth date. The expiry-date check (index 27) is
	// deliberately NOT required — a misread expiry, which the app never stores,
	// must not discard an otherwise valid, checksum-proven MRZ. Two independent
	// mod-10 checks plus the fixed field structure keep false positives ~1/100²·.
	if !mrzCheck(l2[0:9], l2[9]) || !mrzCheck(l2[13:19], l2[19]) {
		return false
	}
	// Sex must be a real MRZ value; guards against a spurious 44-char window.
	switch l2[20] {
	case 'M', 'F', '<':
		return true
	}
	return false
}

func mrzValue(r byte) int {
	if r >= '0' && r <= '9' {
		return int(r - '0')
	}
	if r >= 'A' && r <= 'Z' {
		return int(r-'A') + 10
	}
	return 0
}
func mrzCheck(s string, c byte) bool {
	if c < '0' || c > '9' {
		return false
	}
	w := []int{7, 3, 1}
	sum := 0
	for i := 0; i < len(s); i++ {
		sum += mrzValue(s[i]) * w[i%3]
	}
	return sum%10 == int(c-'0')
}
func mrzDate(s string, birth bool) string {
	if len(s) != 6 {
		return ""
	}
	yy, _ := strconv.Atoi(s[:2])
	mm, _ := strconv.Atoi(s[2:4])
	dd, _ := strconv.Atoi(s[4:6])
	year := 2000 + yy
	if birth && year > time.Now().Year() {
		year -= 100
	}
	if !birth && year < time.Now().Year()-10 {
		year += 100
	}
	if mm < 1 || mm > 12 || dd < 1 || dd > 31 {
		return ""
	}
	return fmt.Sprintf("%02d/%02d/%04d", dd, mm, year)
}

func parsePassportVisual(text string) Guest {
	u := strings.ToUpper(text)
	g := Guest{}
	// First MRZ line is often readable even if the second line is incomplete.
	for _, l := range mrzCleanLines(u) {
		if p := strings.Index(l, "P<"); p >= 0 && len(l[p:]) >= 5 {
			m := l[p:]
			if len(m) >= 5 {
				g.Nationality = regexp.MustCompile(`[^A-Z]`).ReplaceAllString(m[2:5], "")
			}
			if len(m) > 5 {
				parts := strings.Split(m[5:], "<<")
				sur := strings.ReplaceAll(parts[0], "<", " ")
				giv := ""
				if len(parts) > 1 {
					giv = strings.ReplaceAll(strings.Join(parts[1:], " "), "<", " ")
				}
				g.FullName = cleanName(sur + " " + giv)
			}
			break
		}
	}
	// Passport number candidate plus check digit from a partial second MRZ line.
	for _, l := range mrzCleanLines(u) {
		for i := 0; i+10 <= len(l); i++ {
			cand := []byte(l[i : i+10])
			if !regexp.MustCompile(`\d`).Match(cand) {
				continue
			}
			cand[9] = mrzDigit(cand[9])
			raw := string(cand[:9])
			check := cand[9]
			fixed := repairPassportByCheck(raw, check)
			if fixed != "" {
				g.Passport = fixed
				break
			}
		}
		if g.Passport != "" {
			break
		}
	}
	// Visual passport number label.
	if g.Passport == "" {
		for _, re := range []*regexp.Regexp{regexp.MustCompile(`(?im)(?:PASSPORT(?: NO| NUMBER)?|DOCUMENT NO)\s*[:.]?\s*([A-Z0-9]{6,12})`), regexp.MustCompile(`(?im)\b([A-Z]{1,3}[A-Z0-9]{5,9})\b`)} {
			if m := re.FindStringSubmatch(u); len(m) > 1 {
				g.Passport = normalizePassportOCR(m[1])
				break
			}
		}
	}
	// Date with month name, common on passport visual zones.
	monthRe := regexp.MustCompile(`(?i)\b(\d{1,2})\s+(JAN|FEB|MAR|APR|MAY|JUN|JUL|AUG|SEP|OCT|NOV|DEC)(?:/[A-Z]{3})?\s+(\d{4})\b`)
	if m := monthRe.FindStringSubmatch(u); len(m) == 4 {
		months := map[string]int{"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6, "JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12}
		d, _ := strconv.Atoi(m[1])
		g.BirthDate = fmt.Sprintf("%02d/%02d/%s", d, months[m[2]], m[3])
		g.BirthPrecision = "D"
	}
	if g.BirthDate == "" {
		if m := regexp.MustCompile(`(?im)(?:DATE OF BIRTH|BIRTH DATE|GEBOORTEDATUM)\D{0,30}(\d{1,2}[./-]\d{1,2}[./-]\d{4}|\d{4}[./-]\d{1,2}[./-]\d{1,2})`).FindStringSubmatch(u); len(m) > 1 {
			g.BirthDate = formatDateToken(m[1])
			g.BirthPrecision = "D"
		}
	}
	// The \D gap is wide enough to skip trilingual labels such as "SEX/SEXE/SEXO";
	// bare "M/M" / "F/F" tokens (common when the label OCRs poorly) are also honored.
	if regexp.MustCompile(`(?im)(?:SEX|SEXE|SEXO|GESLACHT)\D{0,20}\bF\b`).MatchString(u) || regexp.MustCompile(`\bF/F\b`).MatchString(u) {
		g.Gender = "F"
	} else if regexp.MustCompile(`(?im)(?:SEX|SEXE|SEXO|GESLACHT)\D{0,20}\bM\b`).MatchString(u) || regexp.MustCompile(`\bM/M\b`).MatchString(u) {
		g.Gender = "M"
	}
	// Label-based surname/given names from the printed zone. This always runs (not
	// only when the MRZ name is empty/garbled), because a truncated MRZ line 1 often
	// yields just the surname — e.g. "NGUYEN" — while the printed "Given names" field
	// still carries the full "TRINITY HOANG". Real passports print the label
	// trilingually (e.g. "Surname/Nom/Apellidos") with the value on the next line, so
	// skip the rest of the label line then capture the first all-caps value line.
	{
		sur := ""
		giv := ""
		if m := regexp.MustCompile(`(?is)(?:SURNAME|FAMILY NAME)\b[^\n]*\n\s*([A-Z][A-Z' \-]{1,40})`).FindStringSubmatch(u); len(m) > 1 {
			sur = m[1]
		}
		if m := regexp.MustCompile(`(?is)GIVEN\s*NAMES?\b[^\n]*\n\s*([A-Z][A-Z' \-]{1,60})`).FindStringSubmatch(u); len(m) > 1 {
			giv = m[1]
		}
		// Same-line variants as a secondary attempt (e.g. "SURNAME: TRAN").
		if sur == "" {
			if m := regexp.MustCompile(`(?im)(?:SURNAME|FAMILY NAME)\s*[:.]?\s*([A-Z][A-Z' \-]{1,40})`).FindStringSubmatch(u); len(m) > 1 {
				sur = m[1]
			}
		}
		if giv == "" {
			if m := regexp.MustCompile(`(?im)GIVEN\s*NAMES?\s*[:.]?\s*([A-Z][A-Z' \-]{1,60})`).FindStringSubmatch(u); len(m) > 1 {
				giv = m[1]
			}
		}
		// Keep whichever of the MRZ-line-1 name and the printed-label name is more
		// complete (more name parts) and not garbled.
		if labelName := cleanName(sur + " " + giv); labelName != "" && !nameLooksGarbled(labelName) {
			g.FullName = preferCompleteName(g.FullName, labelName)
		}
		if nameLooksGarbled(g.FullName) {
			g.FullName = ""
		}
	}
	normalizeGuest(&g)
	return g
}

func repairPassportByCheck(raw string, check byte) string {
	if len(raw) != 9 || check < '0' || check > '9' {
		return ""
	}
	b := []byte(strings.ToUpper(raw))
	amb := map[byte][]byte{'O': {'O', '0'}, '0': {'0', 'O'}, 'I': {'I', '1'}, '1': {'1', 'I'}, 'L': {'L', '1'}, 'B': {'B', '8'}, '8': {'8', 'B'}, 'S': {'S', '5'}, '5': {'5', 'S'}, 'Z': {'Z', '2'}, '2': {'2', 'Z'}, 'G': {'G', '6'}, '6': {'6', 'G'}, 'T': {'T', '1'}}
	var dfs func(int) bool
	dfs = func(i int) bool {
		if i == len(b) {
			return mrzCheck(string(b), check)
		}
		orig := b[i]
		opts := amb[orig]
		if len(opts) == 0 {
			opts = []byte{orig}
		}
		for _, v := range opts {
			b[i] = v
			if dfs(i + 1) {
				return true
			}
		}
		b[i] = orig
		return false
	}
	if dfs(0) {
		return string(b)
	}
	return ""
}
func normalizePassportOCR(s string) string {
	s = strings.ToUpper(regexp.MustCompile(`[^A-Z0-9]`).ReplaceAllString(s, ""))
	return s
}

func convertXlsToCSV(in, out string) error {
	return runPowerShell(`param([string]$InputPath,[string]$OutputPath)$ErrorActionPreference='Stop';$e=New-Object -ComObject Excel.Application;$e.Visible=$false;$e.DisplayAlerts=$false;try{$w=$e.Workbooks.Open($InputPath);$w.Worksheets.Item(1).SaveAs($OutputPath,6);$w.Close($false)}finally{$e.Quit()}`, []string{"-InputPath", in, "-OutputPath", out}, filepath.Dir(out))
}
func convertDocToDocx(in, out string) error {
	return runPowerShell(`param([string]$InputPath,[string]$OutputPath)$ErrorActionPreference='Stop';$w=New-Object -ComObject Word.Application;$w.Visible=$false;$w.DisplayAlerts=0;try{$d=$w.Documents.Open($InputPath,$false,$true);$d.SaveAs2($OutputPath,16);$d.Close($false)}finally{$w.Quit()}`, []string{"-InputPath", in, "-OutputPath", out}, filepath.Dir(out))
}
func pdfTextWithWord(in, out string) error {
	return runPowerShell(`param([string]$InputPath,[string]$OutputPath)$ErrorActionPreference='Stop';$w=New-Object -ComObject Word.Application;$w.Visible=$false;$w.DisplayAlerts=0;try{$d=$w.Documents.Open($InputPath,$false,$true);[IO.File]::WriteAllText($OutputPath,$d.Content.Text,[Text.UTF8Encoding]::new($false));$d.Close($false)}finally{$w.Quit()}`, []string{"-InputPath", in, "-OutputPath", out}, filepath.Dir(out))
}
func pdfOCRWindows(in, out string) error {
	return runPowerShell(pdfOCRScript, []string{"-InputPath", in, "-OutputPath", out}, filepath.Dir(out))
}
func runPowerShell(script string, args []string, dir string) error {
	p := filepath.Join(dir, "xnc_"+strconv.FormatInt(procSeq.Add(1), 10)+".ps1")
	if err := os.WriteFile(p, []byte("\ufeff"+script), 0644); err != nil {
		return err
	}
	defer os.Remove(p)
	a := []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", p}
	a = append(a, args...)
	cmd := exec.Command("powershell.exe", a...)
	cmd.SysProcAttr = hiddenProcAttr()
	b, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(decodeText(b)))
	}
	return nil
}

func makeTemplateExcel(rows []Guest) ([]byte, error) {
	tpl, err := embedded.ReadFile("template.xlsx")
	if err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(bytes.NewReader(tpl), int64(len(tpl)))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		h := f.FileHeader
		w, err := zw.CreateHeader(&h)
		if err != nil {
			return nil, err
		}
		b, err := readZipFile(f)
		if err != nil {
			return nil, err
		}
		if f.Name == "xl/worksheets/sheet2.xml" {
			b = fillSheet2(b, rows)
		}
		if _, err = w.Write(b); err != nil {
			return nil, err
		}
	}
	if err = zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
func fillSheet2(b []byte, rows []Guest) []byte {
	s := string(b)
	start := strings.Index(s, "<sheetData>")
	end := strings.Index(s, "</sheetData>")
	if start < 0 || end < 0 {
		return b
	}
	prefix := s[:start+len("<sheetData>")]
	suffix := s[end:]
	// Preserve rows 1-2 exactly from original sheetData.
	sd := s[start+len("<sheetData>") : end]
	idx := 0
	for i := 0; i < 2; i++ {
		j := strings.Index(sd[idx:], "</row>")
		if j < 0 {
			break
		}
		idx += j + len("</row>")
	}
	headRows := sd[:idx]
	var data strings.Builder
	data.WriteString(headRows)
	for i, g := range rows {
		r := i + 3
		vals := []string{strconv.Itoa(i + 1), g.FullName, g.BirthDate, precisionLabel(g.BirthPrecision), genderLabel(g.Gender), g.Nationality, g.Passport, g.Room, g.Arrival, g.Departure, g.Checkout}
		data.WriteString(fmt.Sprintf(`<row r="%d" spans="1:11">`, r))
		for c, v := range vals {
			ref := colName(c+1) + strconv.Itoa(r)
			style := "5"
			if c == 0 {
				style = "6"
			}
			data.WriteString(fmt.Sprintf(`<c r="%s" s="%s" t="inlineStr"><is><t>%s</t></is></c>`, ref, style, xmlEscape(v)))
		}
		data.WriteString(`</row>`)
	}
	return []byte(prefix + data.String() + suffix)
}
func colName(n int) string {
	s := ""
	for n > 0 {
		n--
		s = string(rune('A'+n%26)) + s
		n /= 26
	}
	return s
}
func xmlEscape(s string) string { return html.EscapeString(s) }
func precisionLabel(s string) string {
	switch strings.ToUpper(s) {
	case "D":
		return "D - Ngày"
	case "M":
		return "M - Tháng"
	case "Y":
		return "Y - Năm"
	}
	return s
}
func genderLabel(s string) string {
	if strings.ToUpper(s) == "M" {
		return "M - Nam"
	}
	if strings.ToUpper(s) == "F" {
		return "F - Nữ"
	}
	return s
}

const imageOCRScript = `param([Parameter(Mandatory=$true)][string]$InputPath,[Parameter(Mandatory=$true)][string]$OutputPath)
$ErrorActionPreference='Stop';Add-Type -AssemblyName System.Runtime.WindowsRuntime
function Get-AsTaskMethod([bool]$Generic){[System.WindowsRuntimeSystemExtensions].GetMethods()|?{$_.Name-eq'AsTask'-and$_.GetParameters().Count-eq 1-and$_.IsGenericMethod-eq$Generic}|select -First 1}
function Await($Op,[Type]$T){$m=Get-AsTaskMethod $true;$task=$m.MakeGenericMethod($T).Invoke($null,@($Op));$task.Wait();$task.Result}
$sf=[Windows.Storage.StorageFile,Windows.Storage,ContentType=WindowsRuntime];$dec=[Windows.Graphics.Imaging.BitmapDecoder,Windows.Graphics.Imaging,ContentType=WindowsRuntime];$sb=[Windows.Graphics.Imaging.SoftwareBitmap,Windows.Graphics.Imaging,ContentType=WindowsRuntime];$oe=[Windows.Media.Ocr.OcrEngine,Windows.Foundation,ContentType=WindowsRuntime];$lang=[Windows.Globalization.Language,Windows.Globalization,ContentType=WindowsRuntime]
$e=$null;try{$en=[Activator]::CreateInstance($lang,@('en-US'));if($oe::IsLanguageSupported($en)){$e=$oe::TryCreateFromLanguage($en)}}catch{};if(!$e){$e=$oe::TryCreateFromUserProfileLanguages()};if(!$e){throw 'Windows OCR không khả dụng'}
$f=Await($sf::GetFileFromPathAsync($InputPath)) $sf;$st=Await($f.OpenReadAsync()) ([Windows.Storage.Streams.IRandomAccessStreamWithContentType,Windows.Storage.Streams,ContentType=WindowsRuntime]);try{$d=Await($dec::CreateAsync($st)) $dec;$b=Await($d.GetSoftwareBitmapAsync()) $sb;try{$rt=[Windows.Media.Ocr.OcrResult,Windows.Foundation,ContentType=WindowsRuntime];$r=Await($e.RecognizeAsync($b)) $rt;[IO.File]::WriteAllText($OutputPath,[string]$r.Text,[Text.UTF8Encoding]::new($false))}finally{if($b){$b.Dispose()}}}finally{if($st){$st.Dispose()}}`

const pdfOCRScript = `param([Parameter(Mandatory=$true)][string]$InputPath,[Parameter(Mandatory=$true)][string]$OutputPath)
$ErrorActionPreference='Stop';Add-Type -AssemblyName System.Runtime.WindowsRuntime
function GM([bool]$g){[System.WindowsRuntimeSystemExtensions].GetMethods()|?{$_.Name-eq'AsTask'-and$_.GetParameters().Count-eq 1-and$_.IsGenericMethod-eq$g}|select -First 1};function Await($o,[Type]$t){$m=GM $true;$x=$m.MakeGenericMethod($t).Invoke($null,@($o));$x.Wait();$x.Result};function AwaitA($o){$m=GM $false;$x=$m.Invoke($null,@($o));$x.Wait()}
$sf=[Windows.Storage.StorageFile,Windows.Storage,ContentType=WindowsRuntime];$pd=[Windows.Data.Pdf.PdfDocument,Windows.Data.Pdf,ContentType=WindowsRuntime];$ro=[Windows.Data.Pdf.PdfPageRenderOptions,Windows.Data.Pdf,ContentType=WindowsRuntime];$mem=[Windows.Storage.Streams.InMemoryRandomAccessStream,Windows.Storage.Streams,ContentType=WindowsRuntime];$dec=[Windows.Graphics.Imaging.BitmapDecoder,Windows.Graphics.Imaging,ContentType=WindowsRuntime];$sb=[Windows.Graphics.Imaging.SoftwareBitmap,Windows.Graphics.Imaging,ContentType=WindowsRuntime];$oe=[Windows.Media.Ocr.OcrEngine,Windows.Foundation,ContentType=WindowsRuntime];$e=$oe::TryCreateFromUserProfileLanguages();if(!$e){throw 'Windows OCR không khả dụng'}
$f=Await($sf::GetFileFromPathAsync($InputPath)) $sf;$pdf=Await($pd::LoadFromFileAsync($f)) $pd;$parts=@();for($i=0;$i-lt$pdf.PageCount;$i++){$p=$pdf.GetPage($i);$st=[Activator]::CreateInstance($mem);try{$opt=[Activator]::CreateInstance($ro);if($p.Size.Height-ge$p.Size.Width){$opt.DestinationHeight=[uint32]2600}else{$opt.DestinationWidth=[uint32]2600};AwaitA($p.RenderToStreamAsync($st,$opt));$st.Seek(0);$d=Await($dec::CreateAsync($st)) $dec;$b=Await($d.GetSoftwareBitmapAsync()) $sb;try{$rt=[Windows.Media.Ocr.OcrResult,Windows.Foundation,ContentType=WindowsRuntime];$r=Await($e.RecognizeAsync($b)) $rt;$parts+=[string]$r.Text}finally{if($b){$b.Dispose()}}}finally{if($st){$st.Dispose()};if($p){$p.Dispose()}}};[IO.File]::WriteAllText($OutputPath,($parts-join([Environment]::NewLine+[Environment]::NewLine)),[Text.UTF8Encoding]::new($false))`

const passportPrepScript = `param([string]$InputPath,[string]$OutputDir,[string]$ListPath)
$ErrorActionPreference='Stop';Add-Type -AssemblyName System.Drawing
function Save-ScaledCrop($src,[double]$l,[double]$t,[double]$r,[double]$b,[string]$name,[int]$target=2800){$x=[Math]::Max(0,[int]($src.Width*$l));$y=[Math]::Max(0,[int]($src.Height*$t));$w=[Math]::Min($src.Width-$x,[int]($src.Width*($r-$l)));$h=[Math]::Min($src.Height-$y,[int]($src.Height*($b-$t)));if($w-lt 20-or$h-lt 20){return $null};$rect=New-Object Drawing.Rectangle($x,$y,$w,$h);$c=$src.Clone($rect,$src.PixelFormat);try{$tw=[Math]::Max($w,$target);$th=[int]($h*$tw/$w);$o=New-Object Drawing.Bitmap($tw,$th);$g=[Drawing.Graphics]::FromImage($o);$g.InterpolationMode=[Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic;$g.SmoothingMode=[Drawing.Drawing2D.SmoothingMode]::HighQuality;$g.DrawImage($c,0,0,$tw,$th);$g.Dispose();$p=Join-Path $OutputDir $name;$o.Save($p,[Drawing.Imaging.ImageFormat]::Png);$o.Dispose();return $p}finally{$c.Dispose()}}
$img=[Drawing.Image]::FromFile($InputPath);try{$orientation=1;try{$orientation=$img.GetPropertyItem(274).Value[0]}catch{};switch($orientation){3{$img.RotateFlip([Drawing.RotateFlipType]::Rotate180FlipNone)}6{$img.RotateFlip([Drawing.RotateFlipType]::Rotate90FlipNone)}8{$img.RotateFlip([Drawing.RotateFlipType]::Rotate270FlipNone)}};$bmp=New-Object Drawing.Bitmap($img.Width,$img.Height);$g=[Drawing.Graphics]::FromImage($bmp);$g.DrawImage($img,0,0,$img.Width,$img.Height);$g.Dispose();$files=New-Object System.Collections.Generic.List[string]
# Detect the longest bright horizontal run. This removes black phone screenshot borders.
$sw=160;$sh=[Math]::Max(40,[int]($bmp.Height*$sw/$bmp.Width));$sm=New-Object Drawing.Bitmap($sw,$sh);$sg=[Drawing.Graphics]::FromImage($sm);$sg.DrawImage($bmp,0,0,$sw,$sh);$sg.Dispose();$bestS=0;$bestE=$sh;$runS=-1;$bs=0;$be=0;for($yy=0;$yy-lt$sh;$yy++){$sum=0;for($xx=0;$xx-lt$sw;$xx+=4){$c=$sm.GetPixel($xx,$yy);$sum+=($c.R+$c.G+$c.B)/3};$avg=$sum/[Math]::Ceiling($sw/4);$on=$avg-gt 20;if($on-and$runS-lt 0){$runS=$yy};if((!$on-or$yy-eq$sh-1)-and$runS-ge 0){$end=if($on){$yy+1}else{$yy};if(($end-$runS)-gt($be-$bs)){$bs=$runS;$be=$end};$runS=-1}};$sm.Dispose();$top=[Math]::Max(0,$bs/$sh);$bottom=[Math]::Min(1,$be/$sh);if(($bottom-$top)-lt .25){$top=0;$bottom=1}
$doc=Save-ScaledCrop $bmp 0 $top 1 $bottom 'document.png' 2400;if($doc){$files.Add($doc)}
# Primary MRZ and visual regions relative to detected document.
$docBmp=$null;if($doc){$docBmp=[Drawing.Bitmap]::FromFile($doc)}else{$docBmp=$bmp}
try{foreach($z in @(@(.66,.99,'mrz_document.png'),@(.52,.76,'mrz_mid.png'),@(.72,.98,'mrz_low.png'))){$p=Save-ScaledCrop $docBmp 0 ([double]$z[0]) 1 ([double]$z[1]) ([string]$z[2]) 3000;if($p){$files.Add($p)}};$p=Save-ScaledCrop $docBmp .18 .06 .92 .72 'info_document.png' 2800;if($p){$files.Add($p)};$p=Save-ScaledCrop $docBmp .48 .02 .99 .34 'passno_document.png' 2800;if($p){$files.Add($p)}}finally{if($docBmp-ne$bmp){$docBmp.Dispose()}}
# Whole-image bands cover photos where the document fills the frame.
foreach($z in @(@(.40,.66,'mrz_band1.png'),@(.56,.80,'mrz_band2.png'),@(.67,.99,'mrz_band3.png'))){$p=Save-ScaledCrop $bmp 0 ([double]$z[0]) 1 ([double]$z[1]) ([string]$z[2]) 3000;if($p){$files.Add($p)}}
[IO.File]::WriteAllLines($ListPath,$files,[Text.UTF8Encoding]::new($false))}finally{if($bmp){$bmp.Dispose()};$img.Dispose()}`
