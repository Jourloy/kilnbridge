param(
  [Parameter(Mandatory=$true)][string]$ToolsDirectory,
  [Parameter(Mandatory=$true)][ValidatePattern('^[0-9]+\.[0-9]+\.[0-9]+$')][string]$FlutterVersion
)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
New-Item -ItemType Directory -Force -Path $ToolsDirectory | Out-Null
$tools = (Resolve-Path -LiteralPath $ToolsDirectory).Path
$downloads = Join-Path $tools 'downloads'
New-Item -ItemType Directory -Force -Path $downloads | Out-Null

function Get-VerifiedDownload([string]$Url, [string]$Destination, [string]$SHA256) {
  if ($SHA256 -notmatch '^[a-fA-F0-9]{64}$') { throw 'Download is missing a SHA-256 digest.' }
  if ((Test-Path -LiteralPath $Destination) -and (Get-FileHash -LiteralPath $Destination -Algorithm SHA256).Hash -eq $SHA256) { return }
  $partial = "$Destination.partial"
  try {
    Invoke-WebRequest -UseBasicParsing -Uri $Url -OutFile $partial
    if ((Get-FileHash -LiteralPath $partial -Algorithm SHA256).Hash -ne $SHA256) { throw 'Downloaded tool failed its SHA-256 check.' }
    Move-Item -LiteralPath $partial -Destination $Destination -Force
  } finally { Remove-Item -LiteralPath $partial -Force -ErrorAction SilentlyContinue }
}

$gitDirectory = Join-Path $tools 'git'
if (-not (Test-Path -LiteralPath (Join-Path $gitDirectory 'cmd/git.exe'))) {
  Write-Output 'Installing portable Git (no account or repository credentials required)...'
  $archive = Join-Path $downloads 'MinGit-2.55.0.5-64-bit.zip'
  Get-VerifiedDownload 'https://github.com/git-for-windows/git/releases/download/v2.55.0.windows.5/MinGit-2.55.0.5-64-bit.zip' $archive '56d7b226b7693196cfc71fef26568f536c4a021ab6c37ff2db4287bed908e96e'
  $staging = "$gitDirectory.staging"
  Remove-Item -LiteralPath $staging -Recurse -Force -ErrorAction SilentlyContinue
  Expand-Archive -LiteralPath $archive -DestinationPath $staging
  if (-not (Test-Path -LiteralPath (Join-Path $staging 'cmd/git.exe'))) { throw 'Portable Git archive is incomplete.' }
  Remove-Item -LiteralPath $gitDirectory -Recurse -Force -ErrorAction SilentlyContinue
  Move-Item -LiteralPath $staging -Destination $gitDirectory
}
$env:PATH = (Join-Path $gitDirectory 'cmd') + ';' + $env:PATH

$flutterDirectory = Join-Path $tools "flutter/$FlutterVersion"
$flutter = Join-Path $flutterDirectory 'flutter/bin/flutter.bat'
if (-not (Test-Path -LiteralPath $flutter)) {
  Write-Output "Downloading Flutter $FlutterVersion..."
  $releases = Invoke-RestMethod -Uri 'https://storage.googleapis.com/flutter_infra_release/releases/releases_windows.json'
  $release = @($releases.releases | Where-Object { $_.version -eq $FlutterVersion -and $_.channel -eq 'stable' -and $_.dart_sdk_arch -eq 'x64' })
  if ($release.Count -ne 1) { throw "Flutter $FlutterVersion is not an available Windows x64 stable release." }
  $archive = Join-Path $downloads "flutter-$FlutterVersion.zip"
  Get-VerifiedDownload ($releases.base_url + '/' + $release[0].archive) $archive $release[0].sha256
  $staging = "$flutterDirectory.staging"
  Remove-Item -LiteralPath $staging -Recurse -Force -ErrorAction SilentlyContinue
  Expand-Archive -LiteralPath $archive -DestinationPath $staging
  New-Item -ItemType Directory -Force -Path (Split-Path -Parent $flutterDirectory) | Out-Null
  Remove-Item -LiteralPath $flutterDirectory -Recurse -Force -ErrorAction SilentlyContinue
  Move-Item -LiteralPath $staging -Destination $flutterDirectory
}

$vswhere = Join-Path ${env:ProgramFiles(x86)} 'Microsoft Visual Studio/Installer/vswhere.exe'
$vsPath = ''
if (Test-Path -LiteralPath $vswhere) {
  $vsPath = & $vswhere -latest -products '*' -requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 Microsoft.VisualStudio.Component.VC.CMake.Project -property installationPath
}
if (-not $vsPath) {
  Write-Output 'Installing Microsoft C++ Build Tools and Windows SDK. Approve the Windows administrator prompt to continue.'
  $installer = Join-Path $downloads 'vs_buildtools.exe'
  Invoke-WebRequest -UseBasicParsing -Uri 'https://aka.ms/vs/17/release/vs_buildtools.exe' -OutFile $installer
  $signature = Get-AuthenticodeSignature -LiteralPath $installer
  if ($signature.Status -ne 'Valid' -or $signature.SignerCertificate.Subject -notmatch 'O=Microsoft Corporation') { throw 'Microsoft Build Tools signature could not be verified.' }
  $process = Start-Process -FilePath $installer -Verb RunAs -Wait -PassThru -ArgumentList '--passive --wait --norestart --add Microsoft.VisualStudio.Workload.VCTools --includeRecommended'
  if ($process.ExitCode -eq 3010) { throw 'Microsoft Build Tools needs a Windows restart. Restart the PC, then prepare tools again.' }
  if ($process.ExitCode -ne 0) { throw "Microsoft Build Tools exited with code $($process.ExitCode)." }
}

$innoDirectory = Join-Path $tools 'inno'
if (-not (Test-Path -LiteralPath (Join-Path $innoDirectory 'ISCC.exe'))) {
  Write-Output 'Installing Inno Setup...'
  $installer = Join-Path $downloads 'innosetup-6.7.3.exe'
  Get-VerifiedDownload 'https://github.com/jrsoftware/issrc/releases/download/is-6_7_3/innosetup-6.7.3.exe' $installer '9c73c3bae7ed48d44112a0f48e66742c00090bdb5bef71d9d3c056c66e97b732'
  if ((Get-AuthenticodeSignature -LiteralPath $installer).Status -ne 'Valid') { throw 'Inno Setup signature could not be verified.' }
  $process = Start-Process -FilePath $installer -Wait -PassThru -ArgumentList "/VERYSILENT /SUPPRESSMSGBOXES /NORESTART /SP- /CURRENTUSER /DIR=`"$innoDirectory`""
  if ($process.ExitCode -ne 0 -or -not (Test-Path -LiteralPath (Join-Path $innoDirectory 'ISCC.exe'))) { throw 'Inno Setup installation did not complete.' }
}

$env:PUB_CACHE = Join-Path $tools 'pub-cache'
& $flutter config --no-analytics --enable-windows-desktop
if ($LASTEXITCODE -ne 0) { throw 'Flutter configuration failed.' }
& $flutter precache --windows
if ($LASTEXITCODE -ne 0) { throw 'Flutter precache failed.' }
Write-Output 'Windows build tools are ready.'
