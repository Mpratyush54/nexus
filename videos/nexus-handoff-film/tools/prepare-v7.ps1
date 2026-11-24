$ErrorActionPreference='Stop'
$filmRoot='D:/cm-ui/videos/nexus-handoff-film'
$recordRoot='D:/cm-ui/videos/nexus-demo/runtime/recordings'
$filmFFmpeg='C:/Users/Pratyush Mishra/AppData/Local/Microsoft/WinGet/Packages/Gyan.FFmpeg_Microsoft.Winget.Source_8wekyb3d8bbwe/ffmpeg-9.0.1-full_build/bin/ffmpeg.exe'
$archiveRoot=Join-Path $filmRoot 'archives/v6-before-expanded-cut'
if (!(Test-Path -LiteralPath $archiveRoot)) {
    New-Item -ItemType Directory -Path $archiveRoot | Out-Null
    Copy-Item -LiteralPath "$filmRoot/index.html" -Destination $archiveRoot
    Copy-Item -LiteralPath "$filmRoot/compositions" -Destination $archiveRoot -Recurse
}
$filmCuts=@(
    @{Name='memory-demo';File='Nexus-2026-10-25-00-29-03.mkv';Start=55;Duration=80;Crop=$true},
    @{Name='team-demo';File='Nexus-2026-10-25-00-39-35.mkv';Start=19;Duration=25;Crop=$false},
    @{Name='mcp-ui-demo';File='Nexus-2026-10-25-00-39-35.mkv';Start=151;Duration=25;Crop=$false},
    @{Name='recipient-inbox';File='Nexus-2026-10-25-00-45-30.mkv';Start=17;Duration=13;Crop=$false},
    @{Name='recipient-context';File='Nexus-2026-10-25-00-45-30.mkv';Start=46;Duration=24;Crop=$false},
    @{Name='settings-export';File='Nexus-2026-10-25-00-45-30.mkv';Start=151;Duration=15;Crop=$false}
)
foreach($filmCut in $filmCuts) {
    $filmDest=Join-Path $filmRoot "assets/$($filmCut.Name).mp4"
    if(Test-Path -LiteralPath $filmDest) { throw "Refusing to overwrite existing asset: $filmDest" }
    $filmArgs=@('-hide_banner','-loglevel','error','-ss',"$($filmCut.Start)",'-i',(Join-Path $recordRoot $filmCut.File),'-t',"$($filmCut.Duration)",'-map','0:v:0','-an')
    if($filmCut.Crop){$filmArgs+=@('-vf','crop=1920:1020:0:0')}
    $filmArgs+=@('-c:v','libx264','-preset','fast','-crf','12','-pix_fmt','yuv420p','-r','30','-movflags','+faststart',$filmDest)
    & $filmFFmpeg @filmArgs
    if($LASTEXITCODE -ne 0){throw "Encoding failed: $($filmCut.Name)"}
    Write-Output "Prepared $($filmCut.Name)"
}
Copy-Item -LiteralPath 'D:/cm-ui/videos/nexus-demo/runtime/v7/state/mcp-proof.json' -Destination "$filmRoot/assets/mcp-proof.json"
