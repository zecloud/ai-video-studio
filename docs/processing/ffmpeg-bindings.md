# FFmpeg Go binding decision

AI Video Studio needs FFmpeg for thumbnails, probes, non-destructive preview assets, timeline rendering, and final exports. Original camera files are still not persisted locally during ingestion; FFmpeg workflows apply only after assets are in the project library or during explicit editing/export operations.

## Decision

Start with an FFmpeg executable backend and keep `github.com/u2takey/ffmpeg-go` as the first wrapper to adopt when render graph construction becomes complex.

Defer `github.com/obinnaokechukwu/ffgo` until the app needs direct frame-level decode/encode, custom in-memory I/O, or hardware-accelerated libav control that cannot be handled cleanly through FFmpeg command lines.

## Comparison

| Criterion | `u2takey/ffmpeg-go` | `obinnaokechukwu/ffgo` |
|---|---|---|
| License | Apache-2.0 | Apache-2.0 |
| Integration model | Pure-Go DSL that shells out to `ffmpeg`/`ffprobe` executables | purego dynamic calls into FFmpeg `libav*` shared libraries, with optional shim binaries |
| Build impact | Simple Go builds; no CGO; no libav linking in the app binary | No CGO, but runtime dynamic libraries and shim loading must be packaged/validated |
| Desktop packaging | Bundle or detect `ffmpeg.exe` and `ffprobe.exe`; easier Windows/macOS/Linux story | Must bundle/discover compatible FFmpeg shared libraries per OS/arch |
| Probe/thumbnails | Supported through `ffprobe` and `ffmpeg` command graphs | Supported through native-style APIs |
| Trim/concat/render | Fluent command graph is a good fit | Possible, but higher packaging and API risk |
| Progress/cancel/logs | Process-level progress via `-progress`, stderr logs, context cancellation | Richer callbacks possible, but requires deeper runtime integration |
| Custom I/O | Limited to FFmpeg pipe/stdio patterns | Better fit for `io.Reader`/`io.Writer` and frame-level processing |
| License obligations | FFmpeg executable/codecs still require LGPL/GPL review, but app avoids direct libav linkage | Direct libav distribution/loading makes LGPL/GPL review more involved |

## Implementation shape

The current prototype adds `internal/videoprocessing.CLIProcessor` behind the existing `VideoProcessor` interface:

- `RuntimeStatus` validates both `ffmpeg` and `ffprobe`.
- `Probe` parses `ffprobe` JSON into `ProbeResult`.
- `Thumbnail` builds a bounded command for a single JPEG extraction.
- `Render` builds and runs a non-destructive FFmpeg command from `RenderRequest` clips: per-clip trim, video concat, optional scaling, optional centered text overlays, H.264 output by default, context cancellation, and returned logs/error state.
- `CompareBindings` exposes the binding decision to the Wails service layer.

This keeps the app shippable without committing to a specific third-party wrapper yet. The app can later replace CLI command construction with `ffmpeg-go` while preserving the public `VideoProcessor` interface.

## Packaging requirements

- Detect configured `ffmpegPath` and `ffprobePath` first, then fall back to `PATH`.
- Document that LGPL builds of FFmpeg are preferred unless a GPL codec/filter is explicitly required.
- Do not hide FFmpeg availability; surface missing binary/library status in the UI and settings.
- Validate Windows, macOS, and Linux packaging before enabling render/export features by default.

## Azure render worker packaging

The asynchronous cloud renderer runs in a dedicated private Container App image built from `azure-video-indexer-service/Dockerfile.ffmpeg`. The API and indexing worker image also installs FFmpeg so `VideoIndexerNormalize` can run `ffprobe` and validate media signals before creating edit suggestions.

- The image uses Alpine's distribution `ffmpeg` package and runs the service as a non-root user.
- The deployment sets `SERVICE_ROLE=ffmpeg-worker`, `FFMPEG_PATH=/usr/bin/ffmpeg`, `RENDER_WORKSPACE_ROOT=/render-work`, and `RENDER_TIMEOUT=2h`.
- Each 2 vCPU / 4 GiB worker has an ephemeral `EmptyDir` workspace. Container Apps limits this replica size to 8 GiB of ephemeral storage; render operations must keep their aggregate temporary working set within the documented 6 GiB operational budget. API-side admission enforcement is tracked separately.
- The image policy is LGPL-oriented: the worker uses FFmpeg's native MPEG-4 encoder and must not select GPL `libx264` or `libx265`. Verify the packaged FFmpeg build configuration and retain applicable package and FFmpeg notices before production release.
- The worker receives only Blob references and uses its managed identity for Blob and Durable Task Scheduler access. It never receives delegated OneDrive credentials.
