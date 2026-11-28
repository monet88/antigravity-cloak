$ErrorActionPreference = 'Stop'

$unfmt = gofmt -l (git ls-files --cached --others --exclude-standard '*.go')
if ($unfmt) {
    Write-Error "Unformatted Go files:`n$($unfmt -join "`n")"
    exit 1
}

go vet ./...
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

go test -race -count=1 ./...
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

go test -count=1 ./.github/scripts
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

python -m compileall -q tests
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
