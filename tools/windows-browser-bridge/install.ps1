$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$bin = Join-Path $root "bin"
New-Item -ItemType Directory -Force -Path $bin | Out-Null

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw "Go is required to build ego-browser.exe"
}
if (-not (Get-Command node -ErrorAction SilentlyContinue)) {
    throw "Node 20+ is required to run the bridge"
}

Push-Location $root
$hadGocache = Test-Path Env:GOCACHE
$originalGocache = $env:GOCACHE
$hadGomodcache = Test-Path Env:GOMODCACHE
$originalGomodcache = $env:GOMODCACHE
$tempRoot = if ($env:TEMP) { $env:TEMP } else { [System.IO.Path]::GetTempPath() }
$goCache = Join-Path $tempRoot "judgment-media-go-cache"
$goModCache = Join-Path $tempRoot "judgment-media-go-mod-cache"
try {
    New-Item -ItemType Directory -Force -Path $goCache | Out-Null
    New-Item -ItemType Directory -Force -Path $goModCache | Out-Null
    $env:GOCACHE = $goCache
    $env:GOMODCACHE = $goModCache
    $outputPath = Join-Path $bin "ego-browser.exe"
    go build -o $outputPath .\cmd\ego-browser-launcher
    if ($LASTEXITCODE -ne 0) {
        throw "go build failed with exit code $LASTEXITCODE"
    }
    if (-not (Test-Path -LiteralPath $outputPath -PathType Leaf)) {
        throw "go build completed without producing $outputPath"
    }
} finally {
    if ($hadGocache) {
        $env:GOCACHE = $originalGocache
    } else {
        Remove-Item Env:GOCACHE -ErrorAction SilentlyContinue
    }
    if ($hadGomodcache) {
        $env:GOMODCACHE = $originalGomodcache
    } else {
        Remove-Item Env:GOMODCACHE -ErrorAction SilentlyContinue
    }
    Pop-Location
}

$exe = Join-Path $bin "ego-browser.exe"
Write-Host "Built $exe"
Write-Host "Set the publisher command without changing PATH:"
Write-Host ('$env:VIDEO_PUBLISHER_V2_EGO_COMMAND = "' + $exe + '"')
Write-Host "Install dependencies from this directory with: npm install"
