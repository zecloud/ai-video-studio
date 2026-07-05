# Copilot Instructions

## Project Overview

AI Video Studio is a desktop application for importing DJI Osmo Action 4 footage, securing originals in OneDrive 365, analyzing videos with Azure Content Understanding, and preparing non-destructive AI-assisted edits.

Primary user goal: connect to an Osmo Action 4 over DJI Wi-Fi/BLE, browse videos on the camera, select clips, and stream them directly to OneDrive without storing full original video files on the local disk.

## Architecture

Use **Wails v3** with a Go backend and a minimal TypeScript/Vite frontend.

Core services planned in Go:

- `CameraService`: discover/test the Osmo media endpoint, list media, open ranged HTTP streams from the camera.
- `DJIControlService`: BLE pairing, Wi-Fi/AP setup, DUML/UDP diagnostics, and camera status using Go ports/adaptations of DJI protocol references.
- `TransferService`: orchestrate camera-to-cloud streaming, chunk progress, retry, cancellation, and resumable transfer state.
- `OneDriveService`: Microsoft Graph delegated auth and chunked upload sessions for OneDrive 365.
- `ContentUnderstandingService`: submit uploaded videos to Azure Content Understanding and normalize scenes, transcripts, highlights, and edit suggestions.
- `EditingService`: manage non-destructive edit projects, timelines, clip decisions, transitions, titles, and render jobs.
- `VideoProcessingService`: expose a `VideoProcessor` abstraction; compare `ffmpeg-go` and `ffgo` before selecting the backend.
- `ProjectLibraryService`: associate OneDrive assets, Azure analysis metadata, and editing projects.
- `SettingsService`: store non-sensitive preferences such as tenant/client ID, chunk size, destination folder, and FFmpeg path.

## External References and Constraints

- Wails v3 is required. Do not scaffold or migrate to Wails v2 unless the user explicitly changes direction.
- DJI protocol references:
  - `xaionaro-go/djictl` is CC0 and useful for Go BLE/Wi-Fi/DUML concepts.
  - `datagutt/node-osmo` is MIT and directly targets Osmo Action 3/4/5 BLE behavior; port ideas to Go instead of embedding Node by default.
  - `SemiConscious/osmo-download` has no license identified; use only as protocol inspiration and do not copy code.
- Likely Osmo media endpoint to validate on real hardware: `http://192.168.2.1/v2?storage={0|1}&path=<path>` with `HEAD` and `Range` support.
- Microsoft Graph should start with least privilege. Prefer delegated `Files.ReadWrite.AppFolder`; only broaden to `Files.ReadWrite` with an explicit product reason.
- Desktop auth must use public-client OAuth with PKCE or device code. Never embed a client secret.
- Azure Content Understanding may require an accessible HTTPS source. If OneDrive URLs are not accepted directly, use Azure Blob staging with short-lived SAS and explicit retention/deletion.
- FFmpeg is a runtime dependency regardless of binding. Document and validate binary/library detection, packaging, codec support, and LGPL/GPL implications.

## Data and Storage Rules

- Original camera videos must not be persisted as complete local files during ingestion.
- Stream camera reads into bounded memory chunks and upload those chunks to Microsoft Graph upload sessions.
- Local persistence is allowed for non-sensitive settings, transfer metadata, thumbnails/proxies, analysis metadata, edit decision lists, logs, and explicit render/export outputs.
- Rendering and previews may require temporary files; keep that behavior scoped to editing/export workflows and make it visible in UX and settings.
- Never commit credentials, tokens, tenant secrets, SAS URLs, or generated local caches.

## Commands

These commands are the intended project commands once the Wails/Vite scaffold exists:

```bash
# Install frontend dependencies
npm install --prefix frontend

# Run the frontend checks
npm run check --prefix frontend

# Build the frontend
npm run build --prefix frontend

# Run Go tests
go test ./...

# Run the desktop app in development
task dev

# Build the desktop app
task app:build

# Regenerate Wails TypeScript bindings after service signature changes
wails3 generate bindings -ts
```

If `go`, `wails3`, or platform WebView dependencies are missing locally, do not fake validation. State the missing dependency and validate the parts that can run.

## Frontend and Design Conventions

- Product UI, not marketing UI: dense, readable, trustworthy, and task-first.
- Use TypeScript with strict checks and vanilla/Vite unless the user approves another frontend framework.
- Maintain `PRODUCT.md` for product register, users, principles, and anti-references.
- Maintain `DESIGN.md` for OKLCH tokens, typography, layout, components, motion, responsive rules, and accessibility.
- Use restrained product styling: neutral surfaces plus one purposeful accent. Avoid decorative glassmorphism, gradient text, hero-metric sections, repetitive generic cards, and tiny uppercase eyebrow scaffolding.
- Body text contrast must be at least 4.5:1; large text at least 3:1; focus states must be visible; keyboard navigation and reduced motion are required.
- Prefer tables, split panes, timelines, queues, and inline diagnostics over modal-heavy flows.

## Go Conventions

- Keep Wails-exposed services thin and typed. Put protocol-specific logic behind interfaces so DJI, Graph, Azure, and FFmpeg implementations remain replaceable.
- Use `context.Context` for network calls, uploads, analysis jobs, and render jobs.
- Return actionable errors instead of swallowing failures or returning success-shaped defaults.
- Model transfer progress and render progress explicitly; emit Wails events for long-running operations.
- Keep cloud SDK clients behind small internal interfaces to make streaming, retry, and cancellation testable.

## Domain Model Conventions

Use these names consistently unless implementation proves a better shape:

- Camera/media: `CameraDevice`, `CameraMediaItem`, `CameraStorage`, `MediaStreamRequest`.
- Transfer/cloud: `TransferJob`, `TransferProgress`, `OneDriveUploadSession`, `CloudAsset`.
- Analysis: `VideoAsset`, `Scene`, `TranscriptSegment`, `HighlightCandidate`, `EditSuggestion`.
- Editing: `EditProject`, `Timeline`, `Track`, `ClipSegment`, `Transition`, `TextOverlay`, `AudioMix`, `RenderPreset`, `RenderJob`.
- Processing: `VideoProcessor`, `ProbeResult`, `ThumbnailRequest`, `RenderRequest`, `RenderProgress`.

## Testing and Validation

- Validate camera assumptions against a real Osmo Action 4 before treating endpoints as stable.
- Test large-file upload using Microsoft Graph upload sessions with sequential `Content-Range` chunks.
- Test transfer interruption/retry behavior without writing full video files locally.
- Test Azure Content Understanding limits for video format, size, duration, and source URL requirements.
- For editing, test probe, thumbnail extraction, trim, concat, render progress, cancellation, logs, and OneDrive upload of final renders.
