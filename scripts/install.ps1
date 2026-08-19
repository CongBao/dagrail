param(
  [string]$Harness = "codex,claude-code,copilot-cli",
  [string]$Version = "latest",
  [string]$Repository = "CongBao/dagrail"
)
$ErrorActionPreference = "Stop"
if ($Version -ne "latest" -and $Version -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$') { throw "Version must be latest or v-prefixed SemVer" }
$arch = if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq "Arm64") { "arm64" } else { "amd64" }
$base = if ($Version -eq "latest") { "https://github.com/$Repository/releases/latest/download" } else { "https://github.com/$Repository/releases/download/$Version" }
$asset = "dagrail_windows_$arch.zip"
$temporary = Join-Path ([System.IO.Path]::GetTempPath()) ("dagrail-install-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $temporary | Out-Null
try {
  Invoke-WebRequest -Uri "$base/$asset" -OutFile (Join-Path $temporary $asset)
  Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile (Join-Path $temporary "checksums.txt")
  $checksumMatches = @((Get-Content (Join-Path $temporary "checksums.txt")) | Where-Object { $_ -match "\s$([regex]::Escape($asset))$" })
  if ($checksumMatches.Count -ne 1) { throw "checksum manifest must contain exactly one asset entry" }
  $expected = $checksumMatches[0].Split()[0]
  $actual = (Get-FileHash (Join-Path $temporary $asset) -Algorithm SHA256).Hash.ToLowerInvariant()
  if (-not $expected -or $actual -ne $expected.ToLowerInvariant()) { throw "checksum verification failed" }
  Add-Type -AssemblyName System.IO.Compression.FileSystem
  $archive = [System.IO.Compression.ZipFile]::OpenRead((Join-Path $temporary $asset))
  try {
    $entries = @($archive.Entries | ForEach-Object { $_.FullName } | Sort-Object)
    $wanted = @("LICENSE", "README.md", "THIRD_PARTY_NOTICES.md", "dagrail.exe") | Sort-Object
    if (($entries -join "`n") -ne ($wanted -join "`n")) { throw "release archive contains unexpected paths" }
  } finally {
    $archive.Dispose()
  }
  Expand-Archive -Path (Join-Path $temporary $asset) -DestinationPath $temporary -Force
  & (Join-Path $temporary "dagrail.exe") plugin install --harness $Harness
  & (Join-Path $temporary "dagrail.exe") plugin runtime-status | Out-Null
  Write-Host "DAGrail installed. Restart open agent applications."
} finally {
  Remove-Item -Recurse -Force $temporary -ErrorAction SilentlyContinue
}
