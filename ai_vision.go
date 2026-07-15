package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// aiEndpoint is a var (not const) so tests can point it at a mock server.
var aiEndpoint = "https://api.anthropic.com/v1/messages"

// aiHTTPClient is shared; the timeout is generous because a vision request on a
// large image can take a while, but it is still bounded so a hung call falls
// back to local OCR instead of blocking the import forever.
var aiHTTPClient = &http.Client{Timeout: 150 * time.Second}

// imageForAI decodes the image, downscales it so the long edge is at most
// maxAIEdge (keeping the request small and fast without hurting legibility) and
// re-encodes it as JPEG. Formats Go can decode (JPEG/PNG) are always normalized;
// undecodable JPEG/PNG bytes are sent as-is; anything else (e.g. TIFF/HEIC) is
// rejected so the caller falls back to the local OCR pipeline.
const maxAIEdge = 2200

func imageForAI(path string) (string, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	img, _, derr := image.Decode(bytes.NewReader(b))
	if derr != nil {
		switch strings.ToLower(filepath.Ext(path)) {
		case ".jpg", ".jpeg":
			return base64.StdEncoding.EncodeToString(b), "image/jpeg", nil
		case ".png":
			return base64.StdEncoding.EncodeToString(b), "image/png", nil
		}
		return "", "", fmt.Errorf("không giải mã được ảnh để gửi AI: %w", derr)
	}
	img = downscaleImage(img, maxAIEdge)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), "image/jpeg", nil
}

// downscaleImage area-averages src down so its long edge is at most maxEdge.
// Area averaging keeps text crisp on downscale far better than nearest-neighbor.
func downscaleImage(src image.Image, maxEdge int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	long := w
	if h > long {
		long = h
	}
	if long <= maxEdge || long == 0 {
		return src
	}
	nw := w * maxEdge / long
	nh := h * maxEdge / long
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		sy0 := b.Min.Y + y*h/nh
		sy1 := b.Min.Y + (y+1)*h/nh
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		for x := 0; x < nw; x++ {
			sx0 := b.Min.X + x*w/nw
			sx1 := b.Min.X + (x+1)*w/nw
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			var rs, gs, bs, cnt uint64
			for sy := sy0; sy < sy1; sy++ {
				for sx := sx0; sx < sx1; sx++ {
					r, g, bb, _ := src.At(sx, sy).RGBA()
					rs += uint64(r >> 8)
					gs += uint64(g >> 8)
					bs += uint64(bb >> 8)
					cnt++
				}
			}
			if cnt == 0 {
				cnt = 1
			}
			dst.SetRGBA(x, y, color.RGBA{uint8(rs / cnt), uint8(gs / cnt), uint8(bs / cnt), 255})
		}
	}
	return dst
}

// aiComplete sends one non-streaming Messages request (optionally with an image)
// and returns the concatenated text. It uses raw net/http to keep the app free
// of external module dependencies, matching the rest of the codebase.
func aiComplete(system, userText, imgB64, mediaType string, maxTokens int) (string, error) {
	key := aiAPIKey()
	if key == "" {
		return "", errors.New("chưa cấu hình Anthropic API key")
	}
	var content []any
	if imgB64 != "" {
		content = append(content, map[string]any{
			"type":   "image",
			"source": map[string]any{"type": "base64", "media_type": mediaType, "data": imgB64},
		})
	}
	content = append(content, map[string]any{"type": "text", "text": userText})
	body := map[string]any{
		"model":      aiModel(),
		"max_tokens": maxTokens,
		"messages":   []any{map[string]any{"role": "user", "content": content}},
	}
	if system != "" {
		body["system"] = system
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, aiEndpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	resp, err := aiHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API lỗi %d: %s", resp.StatusCode, aiErrMessage(rb))
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(rb, &out); err != nil {
		return "", fmt.Errorf("không đọc được phản hồi API: %w", err)
	}
	if out.StopReason == "refusal" {
		return "", errors.New("mô hình từ chối xử lý ảnh này")
	}
	var sb strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	txt := sb.String()
	if strings.TrimSpace(txt) == "" {
		return "", errors.New("phản hồi API rỗng")
	}
	return txt, nil
}

func aiErrMessage(rb []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(rb, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	s := strings.TrimSpace(string(rb))
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// extractJSON pulls the JSON object out of a model reply, tolerating code fences
// or stray prose even though the prompt asks for bare JSON.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:] // drop an optional ```json language tag line
		}
		if k := strings.LastIndex(rest, "```"); k >= 0 {
			rest = rest[:k]
		}
		s = rest
	}
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}

const passportSystem = `You are a precise passport data extractor. Read the passport in the image and return the traveler's details. Follow these rules exactly:
- Transcribe only what is printed. Never guess or invent a value; leave a field as "" if it is not clearly readable.
- fullName: the full name in Latin letters, UPPERCASE, in the order printed with the surname first (as in the machine-readable zone), e.g. "TRAN TONY TRUONG".
- birthDate: date of birth formatted as dd/mm/yyyy.
- gender: "M" or "F".
- nationality: the 3-letter code from the machine-readable zone (e.g. USA, VNM, KOR, TWN).
- passport: the passport/document number exactly as shown, no spaces.
- mrzLine1 and mrzLine2: transcribe the two machine-readable zone lines at the bottom EXACTLY, character for character, using "<" for the filler chevrons. If a line is not clearly visible, use "".
Return ONLY a JSON object with keys fullName, birthDate, gender, nationality, passport, mrzLine1, mrzLine2. No prose, no code fences.`

// extractPassportAI reads one passport via the vision model and cross-checks the
// checksum-protected fields against the transcribed MRZ when it is valid.
func extractPassportAI(path string) (Guest, string, error) {
	imgB64, media, err := imageForAI(path)
	if err != nil {
		return Guest{}, "", err
	}
	txt, err := aiComplete(passportSystem, "Extract this passport.", imgB64, media, 1024)
	if err != nil {
		return Guest{}, "", err
	}
	var r struct {
		FullName    string `json:"fullName"`
		BirthDate   string `json:"birthDate"`
		Gender      string `json:"gender"`
		Nationality string `json:"nationality"`
		Passport    string `json:"passport"`
		MrzLine1    string `json:"mrzLine1"`
		MrzLine2    string `json:"mrzLine2"`
	}
	if err := json.Unmarshal([]byte(extractJSON(txt)), &r); err != nil {
		return Guest{}, "", fmt.Errorf("không phân tích được JSON từ AI: %w", err)
	}
	g := Guest{
		FullName:       r.FullName,
		BirthDate:      r.BirthDate,
		Gender:         r.Gender,
		Nationality:    r.Nationality,
		Passport:       r.Passport,
		BirthPrecision: "D",
	}
	// The MRZ passport number and birth date are each guarded by a check digit.
	// When the model's MRZ transcription passes those checks it is authoritative,
	// so use it to correct any single-character slip in the visual read.
	if strings.TrimSpace(r.MrzLine2) != "" {
		if mg, ok := parseMRZ(r.MrzLine1 + "\n" + r.MrzLine2); ok {
			if mg.Passport != "" && normalizeKey(mg.Passport) != normalizeKey(g.Passport) {
				log.Printf("AI/MRZ passport khác nhau: ai=%q mrz=%q -> dùng MRZ", g.Passport, mg.Passport)
				g.Passport = mg.Passport
			}
			if mg.BirthDate != "" && mg.BirthDate != formatDateToken(g.BirthDate) {
				log.Printf("AI/MRZ ngày sinh khác nhau: ai=%q mrz=%q -> dùng MRZ", g.BirthDate, mg.BirthDate)
				g.BirthDate = mg.BirthDate
			}
			if g.Nationality == "" {
				g.Nationality = mg.Nationality
			}
			if g.Gender == "" {
				g.Gender = mg.Gender
			}
		}
	}
	normalizeGuest(&g)
	warn := ""
	if g.FullName == "" || g.Passport == "" || g.BirthDate == "" {
		warn = "AI đã đọc hộ chiếu nhưng còn thiếu vài ô; vui lòng kiểm tra."
	}
	return g, warn, nil
}

const tableSystem = `You extract foreign-guest rows from a hotel rooming list or passenger table shown in an image. Extract EVERY guest row. For each guest return an object with:
- fullName: full name in Latin letters, UPPERCASE, as printed.
- birthDate: dd/mm/yyyy; if only a year is given use "yyyy"; if unknown use "".
- gender: "M", "F", or "".
- nationality: 3-letter code, or "".
- passport: document number, no spaces, or "".
- room: room number, or "".
- arrival: dd/mm/yyyy, or "".
- departure: dd/mm/yyyy, or "".
Transcribe exactly what is printed; never guess or invent. Include every traveler.
Return ONLY a JSON object of the form {"guests":[ {...}, ... ]}. No prose, no code fences.`

// extractTableAI reads a whole rooming-list / passenger table image into guests.
func extractTableAI(path string) ([]Guest, string, error) {
	imgB64, media, err := imageForAI(path)
	if err != nil {
		return nil, "", err
	}
	txt, err := aiComplete(tableSystem, "Extract every guest from this table.", imgB64, media, 4096)
	if err != nil {
		return nil, "", err
	}
	var r struct {
		Guests []struct {
			FullName    string `json:"fullName"`
			BirthDate   string `json:"birthDate"`
			Gender      string `json:"gender"`
			Nationality string `json:"nationality"`
			Passport    string `json:"passport"`
			Room        string `json:"room"`
			Arrival     string `json:"arrival"`
			Departure   string `json:"departure"`
		} `json:"guests"`
	}
	if err := json.Unmarshal([]byte(extractJSON(txt)), &r); err != nil {
		return nil, "", fmt.Errorf("không phân tích được JSON từ AI: %w", err)
	}
	var out []Guest
	for _, gg := range r.Guests {
		g := Guest{
			FullName:    gg.FullName,
			BirthDate:   gg.BirthDate,
			Gender:      gg.Gender,
			Nationality: gg.Nationality,
			Passport:    gg.Passport,
			Room:        gg.Room,
			Arrival:     gg.Arrival,
			Departure:   gg.Departure,
		}
		normalizeGuest(&g)
		if guestHasData(g) {
			out = append(out, g)
		}
	}
	return out, "", nil
}
