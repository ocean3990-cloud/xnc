package main

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoomingList(t *testing.T) {
	txt := `1 MR LEE/ KUO TSENG 365751437 2034/04/17 1965/12/23 (60)
2 MS LIU/HUICHEN 351493486 2028/12/21 1970/04/11 (56)
3 MR LEE / CHING YANG 353027343 2029/10/22 1996/08/31 (29)
BR385 2026/07/01 14:40/16:50 TPE/HAN`
	r := recordsFromLines(strings.Split(txt, "\n"))
	if len(r) != 3 {
		t.Fatalf("got %d %#v", len(r), r)
	}
	if r[0].Passport != "365751437" || r[0].BirthDate != "23/12/1965" || r[0].Gender != "M" {
		t.Fatalf("bad %#v", r[0])
	}
	if r[1].Gender != "F" {
		t.Fatalf("bad gender %#v", r[1])
	}
}
func TestMRZ(t *testing.T) {
	mrz := `7P<USAPHAM<<ASHLEY<CAT< TUONG<<<<<<<<<<<< A
7 A037354048USA1610132F2709123720000023<234692 SV`
	g, ok := parseMRZ(mrz)
	if !ok {
		t.Fatal("mrz failed")
	}
	if g.Passport != "A03735404" || g.Nationality != "USA" || g.BirthDate != "13/10/2016" || g.Gender != "F" {
		t.Fatalf("%#v", g)
	}
}
func TestPartialPassport(t *testing.T) {
	txt := `P<NLDBUNSKOEK< DERK<<<<<<<<<<<<<<<<<<<<<<<<
NNJO41F786NI 2040M2804132191471550<<<<<56
04 FEB/FEB 1959
M/M`
	g := parsePassportVisual(txt)
	if g.Passport != "NNJ041F78" || g.Nationality != "NLD" || g.BirthDate != "04/02/1959" || g.Gender != "M" {
		t.Fatalf("%#v", g)
	}
}
// Real USA passport (TRAN DIEP THI HOANG) whose MRZ failed in v1.0.9 because a
// single unused expiry-check mismatch / dropped filler discarded the whole MRZ.
func TestRealPassportMRZRobust(t *testing.T) {
	L1 := "P<USATRAN<<DIEP<THI<HOANG<<<<<<<<<<<<<<<<<<<"
	L2 := "A691252571USA7909087F3506053356549008<676742"
	vis := "\nSURNAME/NOM/APELLIDOS\nTRAN\nGIVEN NAMES/PRENOMS/NOMBRES\nDIEP THI HOANG\nDATE OF BIRTH\n08 SEP 1979\nSEX/SEXE/SEXO\nF\n"
	variants := map[string]string{
		"perfect":        L1 + "\n" + L2,
		"expiryMisread":  L1 + "\n" + "A691252571USA7909087F3506083356549008<676742",
		"l2Truncated":    L1 + "\n" + L2[:37],
		"mergedOneLine":  L1 + L2,
		"line1Lost+visl": "1GARBLEDNOCHEVRONLINE1XXXX\n" + L2 + vis,
	}
	for name, txt := range variants {
		g, ok := parseMRZ(txt)
		if !ok {
			t.Fatalf("%s: parseMRZ failed", name)
		}
		if g.FullName == "" {
			if v := parsePassportVisual(txt); v.FullName != "" {
				g.FullName = v.FullName
				normalizeGuest(&g)
			}
		}
		if g.Passport != "A69125257" || g.Nationality != "USA" || g.BirthDate != "08/09/1979" || g.Gender != "F" {
			t.Fatalf("%s: bad %#v", name, g)
		}
		if g.FullName != "TRAN DIEP THI HOANG" {
			t.Fatalf("%s: bad name %q", name, g.FullName)
		}
	}
}

// Real USA passport (PHAM AMY VI): MRZ line-2 is clean but line-1 chevrons were
// OCR'd as letters, so the name came out as "PHAMK KAMY VI KKKK". The name must be
// recovered from the printed visual zone instead of the garbled MRZ line-1.
func TestGarbledMRZNameFallsBackToVisual(t *testing.T) {
	garbledL1 := "P<USAPHAMKKAMYKVIKKKKKKKKKKKKRRRRKKKKKKKKKKK"
	L2 := "A143926249USA0010115F3302059698120353<871526"
	vis := "\nSURNAME/NOM/APELLIDOS\nPHAM\nGIVEN NAMES/PRENOMS/NOMBRES\nAMY VI\n" +
		"NATIONALITY/NATIONALITE/NACIONALIDAD\nUNITED STATES OF AMERICA\n" +
		"DATE OF BIRTH/DATE DE NAISSANCE\n11 OCT 2000\nSEX/SEXE/SEXO\nF\n"
	txt := garbledL1 + "\n" + L2 + vis
	g, ok := parseMRZ(txt)
	if !ok {
		t.Fatal("parseMRZ failed")
	}
	if g.FullName == "" || nameLooksGarbled(g.FullName) {
		if v := parsePassportVisual(txt); v.FullName != "" && !nameLooksGarbled(v.FullName) {
			g.FullName = v.FullName
			normalizeGuest(&g)
		} else if nameLooksGarbled(g.FullName) {
			g.FullName = ""
		}
	}
	if g.Passport != "A14392624" || g.Nationality != "USA" || g.BirthDate != "11/10/2000" || g.Gender != "F" {
		t.Fatalf("bad %#v", g)
	}
	if g.FullName != "PHAM AMY VI" {
		t.Fatalf("name not recovered: %q", g.FullName)
	}
	// Real names must not be misflagged as garbled.
	for _, n := range []string{"TRAN DIEP THI HOANG", "PHAM AMY VI", "NGUYEN VAN ANH", "LEE"} {
		if nameLooksGarbled(n) {
			t.Fatalf("false positive garbled: %q", n)
		}
	}
}

func TestXLSX(t *testing.T) {
	r, err := readXLSX("/mnt/data/XNC_Ocean_Web_v1.0.0/test_input.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	if len(r) == 0 {
		t.Fatal("no rows")
	}
}
func TestExport(t *testing.T) {
	b, err := makeTemplateExcel([]Guest{{FullName: "TEST USER", BirthDate: "01/02/1990", BirthPrecision: "D", Gender: "M", Nationality: "USA", Passport: "A1234567", Room: "101", Arrival: "14/07/2026"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < 10000 {
		t.Fatalf("small %d", len(b))
	}
}

func TestDocxTable(t *testing.T) {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	f, _ := zw.Create("word/document.xml")
	io.WriteString(f, `<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:tbl><w:tr><w:tc><w:p><w:r><w:t>Họ tên</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>Ngày sinh</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>Số hộ chiếu</w:t></w:r></w:p></w:tc></w:tr><w:tr><w:tc><w:p><w:r><w:t>JOHN TEST</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>01/02/1990</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>A1234567</w:t></w:r></w:p></w:tc></w:tr></w:tbl></w:body></w:document>`)
	zw.Close()
	p := filepath.Join(t.TempDir(), "test.docx")
	os.WriteFile(p, b.Bytes(), 0644)
	rows, paras, imgs, err := parseDocx(p, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(paras) == 0 {
		t.Log("no paras expected outside table")
	}
	if len(imgs) != 0 {
		t.Fatal("unexpected images")
	}
	r := recordsFromMatrix(rows)
	if len(r) != 1 || r[0].FullName != "JOHN TEST" || r[0].Passport != "A1234567" {
		t.Fatalf("%#v", r)
	}
}

func TestNestedWordRoomingTable(t *testing.T) {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	f, _ := zw.Create("word/document.xml")
	io.WriteString(f, `<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:tbl>
<w:tr><w:tc><w:p><w:r><w:t>房號</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>NO</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>姓名</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>身分證字號 生日(年齡)</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>護照號碼 有效日期</w:t></w:r></w:p></w:tc></w:tr>
<w:tr><w:tc><w:p><w:r><w:t>01 TWIN</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>001</w:t></w:r></w:p></w:tc><w:tc><w:tbl><w:tr><w:tc><w:p><w:r><w:t>劉 苔珍</w:t></w:r></w:p></w:tc></w:tr><w:tr><w:tc><w:p><w:r><w:t>MS. LIU/TAI CHEN</w:t></w:r></w:p></w:tc></w:tr></w:tbl></w:tc><w:tc><w:p><w:r><w:t>F220774769 1970/08/07(55)</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>365336687 2034/03/05</w:t></w:r></w:p></w:tc></w:tr>
</w:tbl></w:body></w:document>`)
	zw.Close()
	p := filepath.Join(t.TempDir(), "nested.docx")
	os.WriteFile(p, b.Bytes(), 0644)
	rows, _, _, err := parseDocx(p, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := recordsFromWordRows(rows)
	if len(r) != 1 {
		t.Fatalf("got %d %#v", len(r), r)
	}
	g := r[0]
	if g.FullName != "LIU/TAI CHEN" || g.BirthDate != "07/08/1970" || g.Gender != "F" || g.Passport != "365336687" || g.Room != "01" {
		t.Fatalf("bad guest %#v", g)
	}
}

func TestZionSplitNameExcel(t *testing.T) {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	ss, _ := zw.Create("xl/sharedStrings.xml")
	io.WriteString(ss, `<?xml version="1.0"?><sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<si><t>STT</t></si><si><t>FULL</t></si><si><t>NAME</t></si><si><t>D.O.B</t></si><si><t>PASSPORT</t></si>
<si><t>LIU</t></si><si><t>TAI</t></si><si><t>CHEN</t></si><si><t>HSUEH</t></si><si><t>CHUN</t></si><si><t>WEI</t></si><si><t>AN</t></si>
</sst>`)
	sh, _ := zw.Create("xl/worksheets/sheet1.xml")
	io.WriteString(sh, `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c><c r="C1" t="s"><v>2</v></c><c r="D1"/><c r="E1" t="s"><v>3</v></c><c r="F1" t="s"><v>4</v></c></row>
<row r="2"><c r="A2"><v>1</v></c><c r="B2" t="s"><v>5</v></c><c r="C2" t="s"><v>6</v></c><c r="D2" t="s"><v>7</v></c><c r="E2"><v>25787</v></c><c r="F2"><v>365336687</v></c></row>
<row r="3"><c r="A3"><v>2</v></c><c r="B3" t="s"><v>7</v></c><c r="C3" t="s"><v>8</v></c><c r="D3" t="s"><v>9</v></c><c r="E3"><v>23218</v></c><c r="F3"><v>3157988810</v></c></row>
<row r="4"><c r="A4"><v>3</v></c><c r="B4" t="s"><v>7</v></c><c r="C4" t="s"><v>10</v></c><c r="D4" t="s"><v>11</v></c><c r="E4"><v>35166</v></c><c r="F4"><v>371087156</v></c></row>
</sheetData></worksheet>`)
	zw.Close()
	p := filepath.Join(t.TempDir(), "zion-layout.xlsx")
	if err := os.WriteFile(p, b.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	r, err := readXLSX(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(r) != 3 {
		t.Fatalf("expected 3 guests, got %d: %#v", len(r), r)
	}
	for i := range r {
		normalizeGuest(&r[i])
	}
	if r[0].FullName != "LIU TAI CHEN" || r[0].BirthDate != "07/08/1970" || r[0].Passport != "365336687" || r[0].BirthPrecision != "D" {
		t.Fatalf("bad first guest: %#v", r[0])
	}
	if r[1].FullName != "CHEN HSUEH CHUN" || r[1].Passport != "3157988810" {
		t.Fatalf("large numeric passport was not preserved: %#v", r[1])
	}
	if r[2].FullName != "CHEN WEI AN" || r[2].BirthDate != "11/04/1996" || r[2].Passport != "371087156" {
		t.Fatalf("bad last guest: %#v", r[2])
	}
}
