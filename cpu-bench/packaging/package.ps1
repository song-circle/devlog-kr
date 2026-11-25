# cpu-bench 포터블 zip 만들기 (설치 없이 풀어서 쓰는 배포본)
#
#   powershell -ExecutionPolicy Bypass -File cpu-bench\packaging\package.ps1 [-SkipTests]
#
# 1) C++ 러너·모듈 Release 빌드(MSVC) + 테스트  2) 화면 앱 self-contained 단일 파일 publish(.NET 설치 불필요)
# 3) dist\cpu-bench-<버전>\ 에 모아 zip  4) SHA-256과 릴리스 설명 초안(release-notes.md) 생성
# 버전은 cpu-bench\VERSION 하나에서 읽는다. 결과물(dist\)은 git에 넣지 않는다.
param([switch]$SkipTests)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot          # cpu-bench
$repo = Split-Path -Parent $root                  # devlog-kr
$version = (Get-Content (Join-Path $root 'VERSION') -TotalCount 1).Trim()
$name = "cpu-bench-$version"
$dist = Join-Path $root 'dist'
$stage = Join-Path $dist $name
$zip = Join-Path $dist "$name-win-x64.zip"

$cmakeDir = "${env:ProgramFiles(x86)}\Microsoft Visual Studio\2022\BuildTools\Common7\IDE\CommonExtensions\Microsoft\CMake\CMake\bin"
if (-not (Get-Command cmake -ErrorAction SilentlyContinue) -and (Test-Path $cmakeDir)) { $env:PATH += ";$cmakeDir" }

function Invoke-Step([string]$what, [scriptblock]$cmd) {
    Write-Host "== $what"
    & $cmd
    if ($LASTEXITCODE -ne 0) { throw "$what failed (exit $LASTEXITCODE)" }
}

Push-Location $root
try {
    Invoke-Step 'cmake configure' { cmake --preset msvc | Out-Null }
    Invoke-Step 'cmake build' { cmake --build --preset msvc }
    if (-not $SkipTests) { Invoke-Step 'ctest' { ctest --preset msvc } }

    $publish = Join-Path $root 'build\publish'
    if (Test-Path $publish) { Remove-Item $publish -Recurse -Force }
    Invoke-Step 'dotnet publish' {
        dotnet publish app\CpuBench.App.csproj -c Release -r win-x64 --self-contained true -o $publish `
            -p:PublishSingleFile=true -p:IncludeNativeLibrariesForSelfExtract=true -p:EnableCompressionInSingleFile=true `
            -p:DebugType=None
    }
} finally { Pop-Location }

# 모으기
if (Test-Path $stage) { Remove-Item $stage -Recurse -Force }
if (Test-Path $zip) { Remove-Item $zip -Force }
New-Item -ItemType Directory -Force $stage | Out-Null
$bin = Join-Path $root 'build\msvc\bin'
Copy-Item (Join-Path $root 'build\publish\cpu-bench.exe') $stage
Copy-Item (Join-Path $bin 'cpu-bench-runner.exe') $stage
Copy-Item (Join-Path $bin 'bench_*.dll') $stage
Copy-Item (Join-Path $repo 'LICENSE') (Join-Path $stage 'LICENSE.txt')
$readme = (Get-Content (Join-Path $PSScriptRoot 'README.txt') -Raw -Encoding UTF8).Replace('{VERSION}', $version)
[IO.File]::WriteAllText((Join-Path $stage 'README.txt'), $readme, (New-Object Text.UTF8Encoding $true))

# 확인: 러너 버전과 앱 파일 버전이 VERSION과 같은지
$runnerVersion = (& (Join-Path $stage 'cpu-bench-runner.exe') version).Trim()
$appVersion = (Get-Item (Join-Path $stage 'cpu-bench.exe')).VersionInfo.ProductVersion
if ($runnerVersion -ne $version) { throw "runner version '$runnerVersion' != VERSION '$version'" }
if (-not $appVersion.StartsWith($version)) { throw "app version '$appVersion' != VERSION '$version'" }

Compress-Archive -Path $stage -DestinationPath $zip
$hash = (Get-FileHash $zip -Algorithm SHA256).Hash.ToLower()
"$hash  $name-win-x64.zip" | Set-Content (Join-Path $dist "$name-win-x64.zip.sha256") -Encoding ascii

$files = Get-ChildItem $stage | ForEach-Object { "| ``$($_.Name)`` | $([math]::Round($_.Length / 1MB, 1)) MB |" }
$notes = @"
## cpu-bench $version

Windows용 무료 진단형 CPU 벤치마크. 설치 없이 압축을 풀고 ``cpu-bench.exe``를 실행하면 됩니다(Windows 10/11 x64, .NET 설치 불필요).

**처음 실행할 때:** 아직 코드 서명 인증서가 없는 초기 버전이라 "Windows의 PC 보호" 창이 뜹니다. **[추가 정보] → [실행]**을 누르세요.

**SHA-256** (``$name-win-x64.zip``)
``````
$hash
``````
PowerShell에서 ``Get-FileHash .\$name-win-x64.zip`` 결과가 위 값과 같으면 원본입니다.

| 파일 | 크기 |
|---|---|
$($files -join "`n")

- PC 측정 결과는 이 PC에만 저장하고 어디에도 보내지 않습니다. 켤 때 GitHub에 최신 버전 번호만 물어봅니다(화면 아래에서 끌 수 있음). [인터넷 속도]를 누르고 동의할 때만 M-Lab과 통신하며, 그 속도 결과와 공인 IP는 M-Lab 공개 데이터로 남습니다.
- 버그 신고·기능 요청: [Issues](https://github.com/song-circle/devlog-kr/issues) · 개발 과정 영상: [버그로그](https://www.youtube.com/@buglog_kr)
"@
[IO.File]::WriteAllText((Join-Path $dist 'release-notes.md'), $notes, (New-Object Text.UTF8Encoding $false))

Write-Host ""
Write-Host "zip    : $zip  ($([math]::Round((Get-Item $zip).Length / 1MB, 1)) MB)"
Write-Host "sha256 : $hash"
Write-Host "notes  : $(Join-Path $dist 'release-notes.md')"
