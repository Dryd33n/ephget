# Builds both executables into dist\. Only ephgetw uses the GUI subsystem.
$ErrorActionPreference = 'Stop'
$out = Join-Path $PSScriptRoot 'dist'
New-Item -ItemType Directory -Force $out | Out-Null
Push-Location $PSScriptRoot
try {
    go build -trimpath -ldflags '-s -w' -o (Join-Path $out 'ephget.exe') ./cmd/ephget
    if ($LASTEXITCODE) { exit $LASTEXITCODE }
    go build -trimpath -ldflags '-s -w -H=windowsgui' -o (Join-Path $out 'ephgetw.exe') ./cmd/ephgetw
    if ($LASTEXITCODE) { exit $LASTEXITCODE }
} finally {
    Pop-Location
}
