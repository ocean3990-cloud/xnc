# XNC Ocean

Ứng dụng khai báo tạm trú cho khách nước ngoài (Windows, WebView2). Đọc dữ liệu
khách từ Excel/Word/PDF/ảnh bảng và **ảnh hộ chiếu**, rồi xuất XML/Excel.

## Đọc hộ chiếu chính xác

Có hai đường bổ trợ nhau (mặc định vẫn chạy OCR nội bộ như trước nếu không cấu hình gì):

- **AI thị giác** (cần internet + Anthropic API key): mở **Cài đặt → Đọc chính xác
  bằng AI**, nhập key `sk-ant-...`. Key lưu tại `%LOCALAPPDATA%\XNC Ocean\config.json`
  hoặc biến môi trường `ANTHROPIC_API_KEY` — **không đưa vào mã nguồn**.
- **Offline — model MRZ**: cài Tesseract-OCR và đặt `mrz.traineddata` (hoặc
  `ocrb.traineddata` / `ocrb_int.traineddata`) vào thư mục `tessdata`
  (`C:\Program Files\Tesseract-OCR\tessdata` hoặc `%LOCALAPPDATA%\XNC Ocean\tessdata`).
  App tự dùng model chuyên font OCR-B cho dòng MRZ; kết hợp kiểm tra checksum để
  đọc số hộ chiếu/ngày sinh chính xác mà vẫn 100% offline.

## Build

Cần **Go 1.23+**. Build ra binary Windows GUI amd64.

### Bản dev (không ký)

```powershell
.\build-release.ps1 -SkipSign
```

Hoặc build thủ công:

```powershell
go build -trimpath -ldflags "-s -w -H windowsgui" -o XNC_Ocean.exe .
```

Ra file trong `dist\` và kèm `..._SHA256.txt`. Chạy trực tiếp, không cần cài đặt.

### Bản release (có ký số)

Đặt bí mật vào **biến môi trường** (không commit lên repo):

```powershell
$env:XNC_PFX_PATH     = 'C:\keys\ocean.pfx'   # đường dẫn chứng thư code-signing
$env:XNC_PFX_PASSWORD = '********'            # mật khẩu PFX
$env:XNC_TIMESTAMP_URL = 'http://timestamp.digicert.com'  # tùy chọn (RFC3161)

.\build-release.ps1
```

Script sẽ: build → ký bằng **SHA256 + timestamp RFC3161** (`sign-release.ps1`) →
tự chạy `signtool verify` → tính SHA256 của **file đã ký**.

- Thiếu `XNC_PFX_PATH` hoặc `XNC_PFX_PASSWORD` khi build release → **báo lỗi rõ và dừng**
  (dùng `-SkipSign` nếu chỉ muốn bản dev chưa ký).
- Cần Windows SDK (Windows Kits 10) để có `signtool.exe`.

Chỉ ký một file đã build sẵn:

```powershell
.\sign-release.ps1 -ExePath .\dist\XNC_Ocean_v1.0.14.exe
```

## Test

```bash
go test ./...
```

> `TestXLSX` đọc một đường dẫn tuyệt đối cố định (`/mnt/data/...`) chỉ có trên máy
> phát triển gốc; nó fail ở nơi khác nhưng không liên quan tới logic app.

## Bảo mật — không commit

`.gitignore` đã chặn: `config.json` (chứa API key), `*.pfx` / `*.p12` / `*.pvk` /
`*.cer` / `*.crt` / `*.key` / `signing.env` (chứng thư & mật khẩu), `*.exe` và
thư mục `dist/` (sản phẩm build).
