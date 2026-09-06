$ErrorActionPreference = "Stop"

& python.exe (Join-Path $PSScriptRoot "migi-document") @args
exit $LASTEXITCODE
