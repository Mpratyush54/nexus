$ErrorActionPreference = 'Stop'
$filmRoot = Split-Path $PSScriptRoot -Parent
$ffmpeg = 'C:/Users/Pratyush Mishra/AppData/Local/Microsoft/WinGet/Packages/Gyan.FFmpeg_Microsoft.Winget.Source_8wekyb3d8bbwe/ffmpeg-9.0.1-full_build/bin/ffmpeg.exe'
$capture = Join-Path $filmRoot 'renders/nexus-shared-context-v7.mp4'
$output = Join-Path $filmRoot 'renders/nexus-shared-context-v7-reviewed.mp4'
$mix = Join-Path $filmRoot '.hyperframes/v7-synchronized-mix.wav'
if (!(Test-Path -LiteralPath $capture)) { throw 'Wait for the picture render to finish.' }
if (Test-Path -LiteralPath $output) { throw 'Review output already exists; will not overwrite it.' }

# Same declared source windows/gains/fades as index.html. The export SFX is
# aligned to (8.1 - 3) / 0.8 + 116.25 = 122.625 seconds after source pacing.
$filter = @'
[0:a]atrim=start=120:end=187.5,asetpts=PTS-STARTPTS,asplit=2[m1][m2];
[m1]volume=0.72,afade=t=in:d=0.12,afade=t=out:st=67.3:d=0.2[ma];
[m2]volume=0.72,afade=t=in:d=0.2,afade=t=out:st=65.5:d=2,adelay=67500|67500[mb];
[1:a]atrim=start=0:end=0.4,asetpts=PTS-STARTPTS,asplit=4[s1][s2][s3][s4];
[s1]volume=0.25,adelay=79620|79620[sa];
[s2]volume=0.25,adelay=88210|88210[sb];
[s3]volume=0.2,adelay=102000|102000[sc];
[s4]volume=0.2,adelay=122625|122625[sd];
[ma][mb][sa][sb][sc][sd]amix=inputs=6:duration=longest:normalize=0,atrim=end=135[mix]
'@
& $ffmpeg -hide_banner -loglevel error -i (Join-Path $filmRoot 'assets/edm-detection-mode.mp3') -i (Join-Path $filmRoot '.media/audio/sfx/sfx_001.mp3') -filter_complex $filter -map '[mix]' -ar 48000 -ac 2 -c:a pcm_s24le -y $mix
if ($LASTEXITCODE -ne 0) { throw 'Audio mix failed.' }
# Two-pass delivery mastering, with 3 dB true-peak headroom for AAC reconstruction.
$measureLog = Join-Path $filmRoot '.hyperframes/v7-master-measure.log'
& $ffmpeg -hide_banner -i $mix -af 'loudnorm=I=-18:TP=-3:LRA=11:print_format=json' -f null NUL 2> $measureLog
if ($LASTEXITCODE -ne 0) { throw 'Loudness measurement failed.' }
$measureText = Get-Content -LiteralPath $measureLog -Raw
$measureJson = [regex]::Match($measureText, '(?s)\{\s*"input_i".*?\}').Value
if (!$measureJson) { throw 'Loudness measurement missing.' }
$stats = $measureJson | ConvertFrom-Json
$normalization = "loudnorm=I=-18:TP=-3:LRA=11:measured_I=$($stats.input_i):measured_TP=$($stats.input_tp):measured_LRA=$($stats.input_lra):measured_thresh=$($stats.input_thresh):offset=$($stats.target_offset):linear=true"
& $ffmpeg -hide_banner -loglevel error -i $capture -i $mix -map 0:v:0 -map 1:a:0 -c:v copy -af $normalization -ar 48000 -c:a aac -b:a 256k -t 135 -movflags +faststart $output
if ($LASTEXITCODE -ne 0) { throw 'Review mux failed.' }
Get-Item -LiteralPath $output | Select-Object FullName,Length
