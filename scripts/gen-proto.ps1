# Generate proto contract code (Windows / PowerShell)
# Usage: scripts/gen-proto.ps1
# Output: backend/internal/proto/<pkg>/v1/*.pb.go + *_grpc.pb.go (same dir as proto)
# Prereq: protoc, protoc-gen-go, protoc-gen-go-grpc in PATH

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot      # D:\Agent
$protoRoot = Join-Path $repoRoot "backend\internal\proto"

# Collect all proto files
$protoFiles = Get-ChildItem -Path $protoRoot -Recurse -Filter "*.proto" |
    ForEach-Object { $_.FullName }
if (-not $protoFiles) {
    Write-Error "[gen-proto] no .proto files found: $protoRoot"
    exit 1
}

Write-Host "[gen-proto] generating:"
$protoFiles | ForEach-Object { Write-Host "  - $_" }

& protoc `
    -I $protoRoot `
    "--go_out=paths=source_relative:$protoRoot" `
    "--go-grpc_out=paths=source_relative:$protoRoot" `
    "--experimental_allow_proto3_optional" `
    @($protoFiles)

if ($LASTEXITCODE -ne 0) {
    Write-Error "[gen-proto] protoc failed (exit=$LASTEXITCODE)"
    exit $LASTEXITCODE
}

Write-Host "[gen-proto] done"
