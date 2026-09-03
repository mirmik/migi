$ErrorActionPreference = "Stop"

& python.exe (Join-Path $PSScriptRoot "migi-file") @args
exit $LASTEXITCODE
