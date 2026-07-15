package main

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestBilingualRoomingListExcel covers Chinese tour "ROOMING LIST" sheets whose
// headers are bilingual English/CJK with line breaks ("NAME 英文姓名", "DOB 出生日期"),
// the header sits below title rows, dates are Excel serials, and Latin names carry
// a "MSTR" title. Before the asciiKey fallback none of these headers matched and
// the whole sheet read as zero guests.
func TestBilingualRoomingListExcel(t *testing.T) {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	ss, _ := zw.Create("xl/sharedStrings.xml")
	io.WriteString(ss, `<?xml version="1.0"?><sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<si><t>ROOMING LIST</t></si>
<si><t>ROOM
房型</t></si>
<si><t>NO</t></si>
<si><t>中文姓名</t></si>
<si><t>NAME
（英文姓名）</t></si>
<si><t>SEX
性别</t></si>
<si><t>DOB
出生日期</t></si>
<si><t>PASSPORT
护照号</t></si>
<si><t>徐晟洋</t></si>
<si><t>XU/SHENGYANG   MSTR</t></si>
<si><t>盛金娟</t></si>
<si><t>SHENG/JINJUAN</t></si>
<si><t>M</t></si>
<si><t>F</t></si>
<si><t>EK8844433</t></si>
<si><t>ED9416673</t></si>
</sst>`)
	sh, _ := zw.Create("xl/worksheets/sheet1.xml")
	io.WriteString(sh, `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
<row r="1"><c r="A1" t="s"><v>0</v></c></row>
<row r="2"><c r="A2" t="s"><v>1</v></c><c r="B2" t="s"><v>2</v></c><c r="C2" t="s"><v>3</v></c><c r="D2" t="s"><v>4</v></c><c r="E2" t="s"><v>5</v></c><c r="F2" t="s"><v>6</v></c><c r="G2" t="s"><v>7</v></c></row>
<row r="3"><c r="B3"><v>1</v></c><c r="C3" t="s"><v>8</v></c><c r="D3" t="s"><v>9</v></c><c r="E3" t="s"><v>12</v></c><c r="F3"><v>42557</v></c><c r="G3" t="s"><v>14</v></c></row>
<row r="4"><c r="B4"><v>2</v></c><c r="C4" t="s"><v>10</v></c><c r="D4" t="s"><v>11</v></c><c r="E4" t="s"><v>13</v></c><c r="F4"><v>31556</v></c><c r="G4" t="s"><v>15</v></c></row>
</sheetData></worksheet>`)
	zw.Close()
	p := filepath.Join(t.TempDir(), "rooming.xlsx")
	if err := os.WriteFile(p, b.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	r, err := readXLSX(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(r) != 2 {
		t.Fatalf("expected 2 guests, got %d: %#v", len(r), r)
	}
	for i := range r {
		normalizeGuest(&r[i])
	}
	if r[0].FullName != "XU/SHENGYANG" || r[0].Gender != "M" || r[0].Passport != "EK8844433" || r[0].BirthDate != "06/07/2016" {
		t.Fatalf("bad first guest: %#v", r[0])
	}
	if r[1].FullName != "SHENG/JINJUAN" || r[1].Gender != "F" || r[1].Passport != "ED9416673" || r[1].BirthDate != "24/05/1986" {
		t.Fatalf("bad second guest: %#v", r[1])
	}
}
