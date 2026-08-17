# A3b KB ingest end-to-end test (temp script, removed after verification)
# Flow: admin login -> create KB -> upload docx/pdf/pptx/blank-pdf -> poll status -> verify -> cleanup
$ErrorActionPreference = 'Stop'
$base = 'http://localhost:8080'
$samples = 'd:\Agent\deploy\data\agent'

function Step([string]$m) { Write-Host "`n=== $m ===" -ForegroundColor Cyan }

# 0. generate a scan-only sample (blank page PDF, no text layer) into shared dir
Step '0 generate blank PDF (scan-only)'
docker exec agent-stack-sandbox-1 python3 -c "import fitz; d=fitz.open(); d.new_page(); d.save('/work/e2e-scan.pdf')"
Write-Host '[OK] e2e-scan.pdf (no text layer)'

# 1. admin login
Step '1 admin login'
$login = Invoke-RestMethod -Method POST -Uri "$base/v1/auth/login" -ContentType 'application/json; charset=utf-8' -Body (@{ username = 'admin'; password = 'Admin@2026' } | ConvertTo-Json -Compress)
$token = [string]$login.access_token
$h = @{ Authorization = "Bearer $token" }
if (-not $token) { Write-Host '[FAIL] admin login'; exit 1 }
Write-Host "[OK] admin token acquired"

# 2. create KB
Step '2 create KB'
$kb = Invoke-RestMethod -Method POST -Uri "$base/v1/admin/kb" -Headers $h -ContentType 'application/json; charset=utf-8' -Body (@{ name = "e2e-parse-check-$(Get-Date -Format 'HHmmss')"; description = 'A3b four formats e2e' } | ConvertTo-Json -Compress)
$kbId = [string]$kb.kb.id
if (-not $kbId) { Write-Host '[FAIL] create KB'; exit 1 }
Write-Host "[OK] kb_id=$kbId"

# 3. upload 4 files
Step '3 upload docx/pdf/pptx/blank'
foreach ($f in @('e2e-sample.docx', 'e2e-sample.pdf', 'e2e-sample.pptx', 'e2e-scan.pdf')) {
    $p = Join-Path $samples $f
    $out = & curl.exe -s -X POST -H "Authorization: Bearer $token" -F "file=@$p" "$base/v1/admin/kb/$kbId/documents"
    Write-Host "  upload $f => $out"
    Start-Sleep -Milliseconds 500
}

# 4. poll until terminal states (max 60s)
Step '4 poll ingest status'
for ($i = 1; $i -le 20; $i++) {
    Start-Sleep -Seconds 3
    # NOTE: use ${kbId} braces — `$kbId?page` would parse `?page` as part of the var name in PS
    $d = Invoke-RestMethod -Method GET -Uri "$base/v1/admin/kb/${kbId}?page=1&page_size=10" -Headers $h
    $status = ($d.kb.documents | ForEach-Object { "$($_.file_name)=$($_.status)" }) -join '  '
    Write-Host "  poll[$i] $status"
    $pending = $d.kb.documents | Where-Object { $_.status -eq 'queued' -or $_.status -eq 'processing' }
    if (-not $pending) { break }
}

# 5. final verification
Step '5 verify terminal states'
$d = Invoke-RestMethod -Method GET -Uri "$base/v1/admin/kb/${kbId}?page=1&page_size=10" -Headers $h
$fail = 0
foreach ($doc in $d.kb.documents) {
    $tag = $null
    if ($doc.file_name -eq 'e2e-sample.docx') { $tag = 'docx' }
    elseif ($doc.file_name -eq 'e2e-sample.pdf') { $tag = 'pdf' }
    elseif ($doc.file_name -eq 'e2e-sample.pptx') { $tag = 'pptx' }
    elseif ($doc.file_name -eq 'e2e-scan.pdf') { $tag = 'scan' }
    if (-not $tag) { continue }
    if ($tag -eq 'scan') {
        if ($doc.status -ne 'failed') { Write-Host "[FAIL] blank-pdf should be failed, got $($doc.status)" -ForegroundColor Red; $fail = 1 }
        elseif (-not $doc.error) { Write-Host "[FAIL] blank-pdf error empty" -ForegroundColor Red; $fail = 1 }
        else { Write-Host "[OK] blank-pdf rejected as expected: $($doc.error)" -ForegroundColor Green }
    } else {
        if ($doc.status -ne 'succeeded') { Write-Host "[FAIL] $($doc.file_name) should be succeeded, got $($doc.status) err=$($doc.error)" -ForegroundColor Red; $fail = 1 }
        else { Write-Host "[OK] $($doc.file_name) succeeded chunks=$($doc.chunk_count)" -ForegroundColor Green }
    }
}

# 6. cleanup: delete KB + temp samples
Step '6 cleanup'
Invoke-RestMethod -Method DELETE -Uri "$base/v1/admin/kb/$kbId" -Headers $h | Out-Null
Remove-Item (Join-Path $samples 'e2e-sample.docx'), (Join-Path $samples 'e2e-sample.pdf'), (Join-Path $samples 'e2e-sample.pptx'), (Join-Path $samples 'e2e-scan.pdf') -Force -ErrorAction SilentlyContinue
docker exec agent-stack-sandbox-1 sh -c "rm -f /work/verify_parsers.sh" | Out-Null
Write-Host '[OK] cleaned kb + temp files'

if ($fail -eq 0) { Write-Host "`nE2E KB ALL PASSED" -ForegroundColor Green } else { Write-Host "`nE2E KB FAILED" -ForegroundColor Red; exit 1 }
