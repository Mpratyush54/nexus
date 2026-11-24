$ErrorActionPreference = 'Stop'
$filmRoot = Split-Path $PSScriptRoot -Parent
$ffmpegDir = 'C:/Users/Pratyush Mishra/AppData/Local/Microsoft/WinGet/Packages/Gyan.FFmpeg_Microsoft.Winget.Source_8wekyb3d8bbwe/ffmpeg-9.0.1-full_build/bin'
$filmOutput = Join-Path $filmRoot 'renders/nexus-shared-context-v7-reviewed.mp4'
$verificationDir = Join-Path $filmRoot '.hyperframes/v7-encoded-review'
New-Item -ItemType Directory -Force -Path $verificationDir | Out-Null
if (!(Test-Path -LiteralPath $filmOutput)) { throw 'Render output is missing.' }

$probeText = & "$ffmpegDir/ffprobe.exe" -v error -show_format -show_streams -of json $filmOutput
if ($LASTEXITCODE -ne 0) { throw 'ffprobe failed.' }
$probe = $probeText | ConvertFrom-Json
$video = @($probe.streams | Where-Object codec_type -eq 'video')[0]
$audio = @($probe.streams | Where-Object codec_type -eq 'audio')[0]
if ($video.width -ne 1920 -or $video.height -ne 1080) { throw 'Unexpected video dimensions.' }
if ($video.r_frame_rate -ne '30/1') { throw 'Unexpected frame rate.' }
if ([math]::Abs([double]$probe.format.duration - 135) -gt .1) { throw 'Unexpected duration.' }
if ($video.codec_name -ne 'h264' -or $audio.codec_name -ne 'aac') { throw 'Expected H.264 and AAC.' }
if ([long]$video.nb_frames -ne 4050) { throw 'Unexpected frame count.' }
$probe | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath (Join-Path $verificationDir 'ffprobe.json')

# Decode the actual MP4, not composition screenshots.
$frameNumbers = @(60,240,420,810,1200,1380,1560,1740,2100,2580,3000,3240,3390,3570,3720,4020)
$selection = ($frameNumbers | ForEach-Object { 'eq(n\,{0})' -f $_ }) -join '+'
& "$ffmpegDir/ffmpeg.exe" -hide_banner -loglevel error -i $filmOutput -vf "select='$selection',scale=480:270,tile=4x4:padding=4:margin=4:color=0x252525" -frames:v 1 -update 1 -y (Join-Path $verificationDir 'encoded-contact-sheet.jpg')
if ($LASTEXITCODE -ne 0) { throw 'Encoded contact-sheet extraction failed.' }

[pscustomobject]@{file=$filmOutput;bytes=(Get-Item -LiteralPath $filmOutput).Length;duration=$probe.format.duration;frames=$video.nb_frames;video=$video.codec_name;audio=$audio.codec_name} | ConvertTo-Json -Compress
