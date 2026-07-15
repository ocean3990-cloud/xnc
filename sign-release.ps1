<#
.SYNOPSIS
    Ký số (code signing) file XNC.exe sau khi build, dùng SignTool.

.DESCRIPTION
    - Đọc đường dẫn PFX và mật khẩu từ BIẾN MÔI TRƯỜNG (không nhúng vào script,
      không commit lên GitHub).
    - Ký bằng SHA256 (/fd SHA256) và đóng dấu thời gian RFC3161 (/tr + /td SHA256).
    - Chạy `signtool verify /pa` sau khi ký để xác nhận chữ ký hợp lệ.

    Biến môi trường:
      XNC_PFX_PATH        (bắt buộc)  Đường dẫn tới file .pfx chứa chứng thư ký.
      XNC_PFX_PASSWORD    (bắt buộc)  Mật khẩu của file .pfx.
      XNC_TIMESTAMP_URL   (tùy chọn)  URL máy chủ timestamp RFC3161.
                                      Mặc định: http://timestamp.digicert.com

    KHÔNG commit file .pfx / .cer / .p12 / .pvk hay mật khẩu vào repo
    (.gitignore đã chặn sẵn các đuôi này).

.PARAMETER ExePath
    Đường dẫn file .exe cần ký. Mặc định: XNC_Ocean_v1.0.14.exe ở thư mục hiện tại.

.PARAMETER TimestampUrl
    Ghi đè URL timestamp; nếu không truyền sẽ lấy từ XNC_TIMESTAMP_URL rồi tới mặc định.

.EXAMPLE
    $env:XNC_PFX_PATH='C:\keys\ocean.pfx'; $env:XNC_PFX_PASSWORD='***'
    .\sign-release.ps1 -ExePath .\XNC_Ocean_v1.0.14.exe
#>

[CmdletBinding()]
param(
    [string]$ExePath = 'XNC_Ocean_v1.0.14.exe',
    [string]$TimestampUrl
)

$ErrorActionPreference = 'Stop'

function Fail([string]$msg) {
    Write-Error $msg
    exit 1
}

# --- Đọc bí mật từ môi trường (không log giá trị) --------------------------
$pfxPath = $env:XNC_PFX_PATH
$pfxPass = $env:XNC_PFX_PASSWORD
if ([string]::IsNullOrWhiteSpace($pfxPath)) { Fail 'Chưa đặt biến môi trường XNC_PFX_PATH (đường dẫn tới file .pfx).' }
if ([string]::IsNullOrWhiteSpace($pfxPass)) { Fail 'Chưa đặt biến môi trường XNC_PFX_PASSWORD (mật khẩu .pfx).' }
if (-not (Test-Path -LiteralPath $pfxPath)) { Fail "Không tìm thấy file PFX: $pfxPath" }
if (-not (Test-Path -LiteralPath $ExePath)) { Fail "Không tìm thấy file cần ký: $ExePath" }

if ([string]::IsNullOrWhiteSpace($TimestampUrl)) {
    $TimestampUrl = if ($env:XNC_TIMESTAMP_URL) { $env:XNC_TIMESTAMP_URL } else { 'http://timestamp.digicert.com' }
}

# --- Tìm signtool.exe ------------------------------------------------------
function Find-SignTool {
    $cmd = Get-Command signtool.exe -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    $roots = @(
        (Join-Path ${env:ProgramFiles(x86)} 'Windows Kits\10\bin'),
        (Join-Path $env:ProgramFiles           'Windows Kits\10\bin'),
        (Join-Path ${env:ProgramFiles(x86)} 'Windows Kits\8.1\bin')
    ) | Where-Object { $_ -and (Test-Path $_) }
    $arch = if ([Environment]::Is64BitOperatingSystem) { 'x64' } else { 'x86' }
    foreach ($root in $roots) {
        $hit = Get-ChildItem -Path $root -Recurse -Filter 'signtool.exe' -ErrorAction SilentlyContinue |
            Where-Object { $_.FullName -match "\\$arch\\" } |
            Sort-Object FullName -Descending | Select-Object -First 1
        if ($hit) { return $hit.FullName }
    }
    return $null
}

$signtool = Find-SignTool
if (-not $signtool) {
    Fail 'Không tìm thấy signtool.exe. Hãy cài Windows SDK (Windows Kits 10) hoặc thêm signtool vào PATH.'
}

$exeFull = (Resolve-Path -LiteralPath $ExePath).Path
Write-Host "SignTool : $signtool"
Write-Host "File     : $exeFull"
Write-Host "Timestamp: $TimestampUrl"
Write-Host "PFX      : $pfxPath  (mật khẩu lấy từ môi trường, không hiển thị)"

# --- Ký: SHA256 + RFC3161 timestamp ---------------------------------------
# /fd SHA256  : thuật toán băm chữ ký là SHA256
# /td SHA256  : thuật toán băm cho dấu thời gian là SHA256
# /tr <url>   : máy chủ timestamp RFC3161 (khác /t là Authenticode cũ)
& $signtool sign `
    /fd SHA256 `
    /f  $pfxPath `
    /p  $pfxPass `
    /tr $TimestampUrl `
    /td SHA256 `
    /v  $exeFull
if ($LASTEXITCODE -ne 0) { Fail "Ký thất bại (signtool sign exit $LASTEXITCODE)." }

# --- Xác minh chữ ký -------------------------------------------------------
# /pa : dùng chính sách xác minh cho ứng dụng độc lập (Default Authenticode)
& $signtool verify /pa /v $exeFull
if ($LASTEXITCODE -ne 0) { Fail "Xác minh thất bại (signtool verify exit $LASTEXITCODE)." }

Write-Host ''
Write-Host "✔ Đã ký và xác minh thành công: $exeFull" -ForegroundColor Green
