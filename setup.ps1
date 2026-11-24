# cactusTrade setup for Windows: downloads the newest release of cactustrade.exe for this
# computer (Intel/AMD or ARM 64-bit) into the current folder, checks it, and replaces an older
# cactustrade.exe there (kept as cactustrade.old.exe). Run it in PowerShell, in the folder
# cactustrade lives in:
#
#   irm https://raw.githubusercontent.com/albievan/cactustrade/main/setup.ps1 | iex
#
# Set $env:CACTUSTRADE_VERSION = "1.2.0" to fetch that release instead of the newest one.
#
# Nothing is installed unless SHA256SUMS carries the release signature (ssh-keygen -Y, PLAN
# 7.9, OD31) of the key below AND the program's checksum is in it. That needs the Windows
# OpenSSH client (ssh-keygen.exe; Settings > Optional features > OpenSSH Client).
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue' # Invoke-WebRequest is many times slower with the bar
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

$repo = 'https://github.com/albievan/cactustrade/releases'
# The public half of the release key: the same line as scripts/allowed_signers (a test checks
# it). Empty until the owner publishes the key: then this script refuses to install.
$SIGNER = ''

$arch = $null
try { $arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString() } catch { }
if (-not $arch) { $arch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE } }
switch -Regex ($arch) {
    '^(X64|AMD64)$' { $arch = 'amd64' }
    '^ARM64$' { $arch = 'arm64' }
    default { throw "cactustrade setup: no release for the $arch processor (there are amd64 and arm64)" }
}
$asset = "cactustrade-windows-$arch.exe"
$base = if ($env:CACTUSTRADE_VERSION) { "$repo/download/v$($env:CACTUSTRADE_VERSION)" } else { "$repo/latest/download" }

if (-not $SIGNER) {
    throw 'cactustrade setup: no release key is published in this setup script yet: nothing can be checked, so nothing is installed (build from source: make build)'
}
$sshkeygen = Get-Command ssh-keygen.exe -ErrorAction SilentlyContinue
if (-not $sshkeygen) {
    throw 'cactustrade setup: ssh-keygen.exe is needed to check the release signature: add the OpenSSH Client (Settings > Optional features)'
}

$tmp = Join-Path ([IO.Path]::GetTempPath()) ("cactustrade-setup-" + [Guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    Write-Host "Downloading $asset ..."
    Invoke-WebRequest -UseBasicParsing -Uri "$base/$asset" -OutFile (Join-Path $tmp $asset)
    Invoke-WebRequest -UseBasicParsing -Uri "$base/SHA256SUMS" -OutFile (Join-Path $tmp 'SHA256SUMS')
    Invoke-WebRequest -UseBasicParsing -Uri "$base/SHA256SUMS.sig" -OutFile (Join-Path $tmp 'SHA256SUMS.sig')
    $signers = Join-Path $tmp 'allowed_signers'
    [IO.File]::WriteAllText($signers, $SIGNER + "`n", (New-Object System.Text.UTF8Encoding($false)))

    # ssh-keygen reads the signed file on stdin: redirect the file itself (a PowerShell pipe
    # would re-encode it and break the signature).
    $sums = Join-Path $tmp 'SHA256SUMS'
    $verifyArgs = @('-Y', 'verify', '-f', "`"$signers`"", '-I', 'cactustrade-release', '-n', 'cactustrade-release', '-s', "`"$(Join-Path $tmp 'SHA256SUMS.sig')`"")
    $p = Start-Process -FilePath $sshkeygen.Source -ArgumentList $verifyArgs -RedirectStandardInput $sums `
        -RedirectStandardOutput (Join-Path $tmp 'verify.out') -RedirectStandardError (Join-Path $tmp 'verify.err') -NoNewWindow -Wait -PassThru
    if ($p.ExitCode -ne 0) { throw 'cactustrade setup: the release signature does not match: not installing' }
    Write-Host 'Release signature: valid.'

    $want = $null
    foreach ($line in Get-Content $sums) {
        $f = $line -split '\s+'
        if ($f.Count -ge 2 -and ($f[1] -eq $asset -or $f[1] -eq "*$asset")) { $want = $f[0] }
    }
    if (-not $want) { throw "cactustrade setup: $asset is not in SHA256SUMS" }
    $got = (Get-FileHash -Algorithm SHA256 (Join-Path $tmp $asset)).Hash.ToLower()
    if ($got -ne $want.ToLower()) { throw "cactustrade setup: the checksum of $asset does not match: not installing" }
    Write-Host 'Checksum: matches.'

    $new = & (Join-Path $tmp $asset) version
    if ($LASTEXITCODE -ne 0) { throw 'cactustrade setup: the downloaded program does not run on this computer' }
    if (Test-Path cactustrade.exe) {
        Move-Item -Force cactustrade.exe cactustrade.old.exe
        Write-Host 'Kept the previous program as cactustrade.old.exe.'
    }
    Move-Item (Join-Path $tmp $asset) cactustrade.exe
    Write-Host "Installed cactustrade $new in $((Get-Location).Path)."
    Write-Host 'Stop cactustrade if it was running and start it again: .\cactustrade.exe (https://localhost:8443).'
    Write-Host 'A first start creates .\data with the SQLite database and master.key (back that key up),'
    Write-Host "and the administrator admin/admin, who must choose a new password. Accounts trade on eToro's"
    Write-Host 'demo by default. See https://github.com/albievan/cactustrade#quick-start-standalone-sqlite'
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
