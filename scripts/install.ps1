param(
  [string]$Harness = "codex,claude-code,copilot-cli",
  [string]$Version = "latest",
  [string]$Repository = "CongBao/dagrail"
)
$ErrorActionPreference = "Stop"
$arch = if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq "Arm64") { "arm64" } else { "amd64" }
$base = if ($Version -eq "latest") { "https://github.com/$Repository/releases/latest/download" } else { "https://github.com/$Repository/releases/download/$Version" }
$asset = "dagrail_windows_$arch.zip"
$temporary = Join-Path ([System.IO.Path]::GetTempPath()) ("dagrail-install-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $temporary | Out-Null
try {
  Invoke-WebRequest -Uri "$base/$asset" -OutFile (Join-Path $temporary $asset)
  Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile (Join-Path $temporary "checksums.txt")
  $expected = ((Get-Content (Join-Path $temporary "checksums.txt")) | Where-Object { $_ -match "\s$([regex]::Escape($asset))$" } | Select-Object -First 1).Split()[0]
  $actual = (Get-FileHash (Join-Path $temporary $asset) -Algorithm SHA256).Hash.ToLowerInvariant()
  if (-not $expected -or $actual -ne $expected.ToLowerInvariant()) { throw "checksum verification failed" }
  Expand-Archive -Path (Join-Path $temporary $asset) -DestinationPath $temporary -Force
  & (Join-Path $temporary "dagrail.exe") plugin install --harness $Harness
  Write-Host "DAGrail installed. Restart open agent applications."
} finally {
  Remove-Item -Recurse -Force $temporary -ErrorAction SilentlyContinue
}
