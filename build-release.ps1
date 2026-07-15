<#
.SYNOPSIS
    Build + (ký) + tính SHA256 cho bản phát hành XNC.exe trong một bước.

.DESCRIPTION
    1. Build binary Windows GUI (amd64) bằng Go.
    2. Ký số bằng sign-release.ps1 (SHA256 + RFC3161 timestamp) — bỏ qua nếu
       chưa có XNC_PFX_PATH/XNC_PFX_PASSWORD hoặc khi truyền -SkipSign.
    3. Tính SHA256 của file .exe CUỐI CÙNG (sau khi ký, vì ký làm đổi nội dung
       file) và ghi ra <exe>_SHA256.txt.

    Bí mật ký (PFX + mật khẩu) đọc từ biến môi trường trong sign-release.ps1;
    không có gì bí mật nằm trong script này.

.PARAMETER Version
    Số phiên bản cho tên file. Mặc định đọc từ `const version = "..."` trong main.go.

.PARAMETER OutDir
    Thư mục xuất. Mặc định 'dist' (đã được .gitignore bỏ qua).

.PARAMETER SkipSign
    Bỏ qua bước ký (build dev). SHA256 khi đó là của file chưa ký.

.PARAMETER TimestampUrl
    Chuyển tiếp cho sign-release.ps1 (URL timestamp RFC3161).

.EXAMPLE
    $env:XNC_PFX_PATH='C:\keys\ocean.pfx'; $env:XNC_PFX_PASSWORD='***'
    .\build-release.ps1

.EXAMPLE
    .\build-release.ps1 -SkipSign        # build dev, không ký
#>

[CmdletBinding()]
param(
    [string]$Version,
    [string]$OutDir = 'dist',
    [switch]$SkipSign,
    [string]$TimestampUrl
)

$ErrorActionPreference = 'Stop'
$root = if ($PSScriptRoot) { $PSScriptRoot } else { (Get-Location).Path }

function Fail([string]$msg) { Write-Error $msg; exit 1 }

# --- Xác định phiên bản ----------------------------------------------------
if ([string]::IsNullOrWhiteSpace($Version)) {
    $mainGo = Join-Path $root 'main.go'
    if (-not (Test-Path -LiteralPath $mainGo)) { Fail "Không tìm thấy main.go ở $root — hãy chạy script trong thư mục repo, hoặc truyền -Version." }
    $m = Select-String -Path $mainGo -Pattern 'const\s+version\s*=\s*"([^"]+)"' | Select-Object -First 1
    if (-not $m) { Fail 'Không đọc được số phiên bản từ main.go; hãy truyền -Version.' }
    $Version = $m.Matches[0].Groups[1].Value
}
Write-Host "Phiên bản: $Version"

# --- Kiểm tra điều kiện ký TRƯỚC khi build (fail nhanh, tránh tạo file chưa ký) ---
$doSign = -not $SkipSign
if ($doSign -and (-not $env:XNC_PFX_PATH -or -not $env:XNC_PFX_PASSWORD)) {
    Fail 'Thiếu biến môi trường để ký: cần cả XNC_PFX_PATH và XNC_PFX_PASSWORD. Hãy đặt hai biến này, hoặc chạy lại với -SkipSign để build bản chưa ký (dev).'
}

# --- Chuẩn bị thư mục xuất -------------------------------------------------
if (-not [System.IO.Path]::IsPathRooted($OutDir)) { $OutDir = Join-Path $root $OutDir }
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
$exe = Join-Path $OutDir "XNC_Ocean_v$Version.exe"

# --- Build -----------------------------------------------------------------
Write-Host "Build   : $exe"
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'
# -H windowsgui: app WebView2, không mở cửa sổ console. -s -w: bỏ bảng ký hiệu.
& go build -trimpath -ldflags '-s -w -H windowsgui' -o $exe $root
if ($LASTEXITCODE -ne 0) { Fail "go build thất bại (exit $LASTEXITCODE)." }

# --- Ký --------------------------------------------------------------------
if ($doSign) {
    $signScript = Join-Path $root 'sign-release.ps1'
    if (-not (Test-Path -LiteralPath $signScript)) { Fail "Không tìm thấy sign-release.ps1 ở $root." }
    $signArgs = @{ ExePath = $exe }
    if ($TimestampUrl) { $signArgs['TimestampUrl'] = $TimestampUrl }
    & $signScript @signArgs
    if ($LASTEXITCODE -ne 0) { Fail "Ký thất bại (sign-release.ps1 exit $LASTEXITCODE)." }
} else {
    Write-Host 'Ký     : (bỏ qua theo -SkipSign)'
}

# --- SHA256 của file cuối cùng --------------------------------------------
$hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $exe).Hash.ToLower()
$shaFile = Join-Path $OutDir "XNC_Ocean_v${Version}_SHA256.txt"
"$hash *XNC_Ocean_v$Version.exe" | Set-Content -LiteralPath $shaFile -Encoding ascii
$sizeMB = [math]::Round((Get-Item -LiteralPath $exe).Length / 1MB, 2)

Write-Host ''
Write-Host "✔ Xong: $exe ($sizeMB MB)" -ForegroundColor Green
Write-Host "  SHA256: $hash"
Write-Host "  Ghi:    $shaFile"
if (-not $doSign) { Write-Host '  (Chưa ký — SHA256 ở trên là của file chưa ký.)' -ForegroundColor Yellow }
