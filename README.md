# qxdl-gentle — polite sequential downloader

A cross-platform CLI designed for "稳妥无感，温和" downloading: single-threaded, per-file interval, random jitter, automatic handling of 429/503 with Retry‑After + backoff, and safe resume-by-skip.

## Build (Windows)
1. Install Go ≥ 1.20: https://go.dev/dl/
2. In this folder:
```
go build -o qxdl.exe
```
(Or `set GOOS=windows && set GOARCH=amd64 && go build -o qxdl.exe`)

## Usage
```
qxdl.exe -url "https://host/path/.../0064.png" -start 0064 -end 0077 -interval 6 -jitter 0.2
```
**Flags (key ones):**
- `-url`       full link to any page in the range
- `-start`     zero-padded start string (e.g., `0064`)
- `-end`       zero-padded or plain end (default = `-start`)
- `-interval`  base seconds between files (default `6`)
- `-jitter`    random jitter fraction (default `0.2` = ±20%)
- `-retries`   retries per file (default `2`)
- `-timeout`   HTTP timeout seconds (default `30`)
- `-max-wait`  cap for adaptive waits (default `300`)
- `-backoff`   multiplier for exponential backoff (default `2.0`)
- `-max-errors` stop after N consecutive failures (default `8`)
- `-ua`        custom User-Agent
- `-quiet`     reduce logs

## Recommended "温和" preset
```
qxdl.exe -url "https://.../0061.png" -start 0061 -end 0074 -interval 6 -jitter 0.2 -retries 2 -max-errors 6
```
This spreads requests irregularly, honors `Retry-After`, and politely backs off on 429/503.
