param([Parameter(Mandatory=$true)][string]$Destination)
$ErrorActionPreference = 'Stop'
$vswhere = Join-Path ${env:ProgramFiles(x86)} 'Microsoft Visual Studio/Installer/vswhere.exe'
$vsPath = & $vswhere -latest -products '*' -requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 -property installationPath
if (-not $vsPath) { throw 'Microsoft C++ tools were not found.' }
$versions = Get-ChildItem -LiteralPath (Join-Path $vsPath 'VC/Redist/MSVC') -Directory |
  Where-Object { $_.Name -match '^\d+\.\d+\.\d+$' } | Sort-Object { [version]$_.Name } -Descending
$runtime = $null
foreach ($version in $versions) {
  $runtime = Get-ChildItem -Path (Join-Path $version.FullName 'x64/Microsoft.VC*.CRT') -Directory |
    Where-Object { Test-Path -LiteralPath (Join-Path $_.FullName 'vcruntime140.dll') } | Select-Object -First 1
  if ($runtime) { break }
}
if (-not $runtime) { throw 'The x64 Microsoft C++ redistributable files are missing. Repair C++ Build Tools.' }
Get-ChildItem -LiteralPath $runtime.FullName -Filter '*.dll' -File |
  Copy-Item -Destination $Destination -Force
Write-Output 'Bundled the Microsoft C++ runtime with the Windows application.'
