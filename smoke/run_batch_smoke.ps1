param(
    [string]$BaseUrl = "http://127.0.0.1:8088",
    [string]$RequestFile = "smoke/batch_products_request.json",
    [string]$ResponseFile = "tmp/batch_smoke_response.json"
)

$ErrorActionPreference = "Stop"

$responseDir = Split-Path -Parent $ResponseFile
if ($responseDir) {
    New-Item -ItemType Directory -Force -Path $responseDir | Out-Null
}

Write-Host "Checking health: $BaseUrl/health"
Invoke-RestMethod -Uri "$BaseUrl/health" -Method Get -TimeoutSec 10 | Out-Null

Write-Host "Running batch request: $RequestFile"
$body = Get-Content -Raw -Encoding UTF8 $RequestFile
$job = Invoke-RestMethod -Uri "$BaseUrl/api/v1/batches/run" -Method Post -ContentType "application/json; charset=utf-8" -Body $body -TimeoutSec 120
$job | ConvertTo-Json -Depth 20 | Set-Content -Encoding UTF8 $ResponseFile

Write-Host "batch_id:" $job.id
Write-Host "status:" $job.status
Write-Host "report_path:" $job.report_path
Write-Host "report_json_path:" $job.report_json_path

foreach ($item in $job.items) {
    Write-Host "item:" $item.index $item.product_title "project_id=$($item.project_id)" "status=$($item.status)" "publish_package=$($item.publish_package_path)"
}

Write-Host "Saved response to $ResponseFile"
