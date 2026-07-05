# AI Video Studio core screens

This screen spec expands the product workflow in [`PRODUCT.md`](../PRODUCT.md) and the UI rules in [`DESIGN.md`](../DESIGN.md). It is documentation-only and assumes the Wails v3 desktop app uses dense product layouts: left navigation, persistent toolbars, split panes, tables, queues, timelines, and inline diagnostics.

## Cross-screen model

- **Navigation:** Onboarding, Camera, Transfers, Library, Analysis, and Editing are first-level destinations in the left rail. The active transfer/render summary remains visible in a compact status strip so long-running work is not hidden by navigation.
- **Progress surfacing:** Use persistent rows, banners, status strips, and details panels instead of modal-heavy UX. Progress must include text labels, percent or byte counts when known, current system, and recovery action.
- **Error surfacing:** Errors are tied to the affected item or panel and name the failing system: camera, OneDrive Graph, Azure Content Understanding, Azure Blob staging, FFmpeg, network, auth, or local runtime.
- **Storage principle:** Original camera videos are streamed in bounded chunks to OneDrive upload sessions. The UI must never imply a full original is written locally. Local persistence is limited to settings, metadata, logs, thumbnails/proxies, edit decisions, and explicit render/export outputs.
- **Accessibility:** All screens support keyboard operation, visible focus, logical tab order, non-color-only status, accessible progress values, live-region announcements for long-running state changes, and reduced-motion behavior.

## Interaction flow

1. **Onboarding/setup** validates Microsoft 365, Azure, camera readiness, FFmpeg, and the zero-full-local-original-storage policy.
2. **Camera media browser** lists Osmo Action 4 media and lets users select clips for transfer.
3. **Transfer queue** streams selected clips to the configured OneDrive app folder and registers completed assets.
4. **Project library** tracks cloud assets, analysis status, edit projects, and renders.
5. **Analysis studio** inspects Azure results and promotes chosen highlights into an edit draft.
6. **Editing studio** refines non-destructive timelines and exports new render assets locally and/or to OneDrive.

Users can move forward when prerequisites are met, or jump back to recover auth, camera, network, analysis, or render failures. Background transfer, analysis, and render jobs continue to surface status through the global strip and relevant rows.

## 1. Onboarding / setup

- **Purpose:** Prepare a trusted workflow before the first transfer: Microsoft 365 auth, Azure Content Understanding settings, camera connection guidance, storage policy, and FFmpeg detection.
- **Layout regions:**
  - Stepper: Microsoft 365, Azure, Camera, Storage policy, FFmpeg.
  - Main validation panel with requirement rows and inline diagnostics.
  - Right details panel showing current permission scope, OneDrive destination, Azure endpoint/analyzer status, and local persistence rules.
- **Primary actions:** Connect Microsoft 365, choose OneDrive app-folder destination, validate Azure settings, run camera diagnostics, locate/test FFmpeg, continue to Camera.
- **Key states:**
  - Empty: no account or service configured; show next required setup step.
  - Loading: auth/device/service checks in progress with labeled spinners or progress text.
  - Error: expired auth, insufficient Graph scope, invalid Azure endpoint/analyzer, camera not reachable, FFmpeg missing; each row includes retry or repair action.
  - Success: all required rows validated and zero-storage policy acknowledged.
- **Keyboard/accessibility notes:** Stepper is keyboard navigable; validation results are announced; disabled Continue has an accessible reason; permission scopes and storage policy are readable without tooltips.
- **Data/service dependencies:** `SettingsService`, `OneDriveService`, `ContentUnderstandingService`, `CameraService`, `DJIControlService`, `VideoProcessingService`.

## 2. Camera media browser

- **Purpose:** Browse DJI Osmo Action 4 inventory, inspect transfer readiness, and select footage without behaving like a generic local file picker.
- **Layout regions:**
  - Top connection/status bar: camera name, Wi-Fi/AP state, endpoint health, battery if available, storage, last refresh.
  - Dense media table: selection, thumbnail, name, type, duration, size, date, storage ID, transfer status, diagnostics.
  - Right preview/details panel: metadata, endpoint path, `HEAD`/`Range` capability, thumbnail/proxy status, warnings.
  - Persistent selection bar: selected count, total bytes, destination, start transfer.
- **Primary actions:** Refresh media, run diagnostics, select all visible, clear selection, start transfer to OneDrive, open transfer queue.
- **Key states:**
  - Empty: no camera connected or no media found; show connect/refresh/diagnostic actions.
  - Loading: scanning storage and fetching thumbnails/metadata.
  - Error: Wi-Fi lost, endpoint unconfirmed, range unsupported, unreadable file, unknown size; table rows remain visible where possible.
  - Success: media rows ready with transfer eligibility and selected clips queued.
- **Keyboard/accessibility notes:** Table supports row focus, range selection, sortable headers, selected-row announcements, and keyboard access to row actions and preview details.
- **Data/service dependencies:** `CameraService`, `DJIControlService`, `ProjectLibraryService` for known/imported status, `TransferService` for queue creation.

## 3. Transfer queue

- **Purpose:** Make large camera-to-OneDrive transfers observable, recoverable, and explicitly non-local.
- **Layout regions:**
  - Global progress header: active/completed/failed counts, current speed, ETA when reliable, destination folder.
  - Queue table: job, source path, size, state, file progress, bytes uploaded, chunk/range, retry count, current system.
  - Details/diagnostics panel: Graph upload session, camera range status, auth state, retry history, logs.
- **Primary actions:** Pause/resume when supported, cancel, retry failed job, reconnect camera, renew auth, open diagnostics, open completed asset in Library.
- **Key states:**
  - Empty: no active transfers; link back to Camera or Library.
  - Loading: preparing upload session, probing range, calculating next chunk.
  - Error: camera disconnect, Graph throttling, token expiry, network interruption, service limit; affected job shows next action.
  - Success: upload completed, cloud asset registered, analysis can be started or queued.
- **Keyboard/accessibility notes:** Progress bars expose `aria-valuenow` where determinate; live region announces state changes; destructive cancel explains whether resume is possible.
- **Data/service dependencies:** `TransferService`, `OneDriveService`, `CameraService`, `SettingsService`, `ProjectLibraryService`.

## 4. Project library

- **Purpose:** Provide the durable workspace for OneDrive assets, Azure analysis outputs, edit projects, and final renders.
- **Layout regions:**
  - Toolbar: search, filters for transfer/analysis/edit/render status, sort, import-from-camera shortcut.
  - Main table: asset/project name, OneDrive status, analysis status, edit status, render status, source device, date, duration, size.
  - Right details panel: metadata, service history, storage location, linked edit projects, recovery actions.
- **Primary actions:** Open analysis, create/edit project, retry analysis, open OneDrive link, view metadata, open render output.
- **Key states:**
  - Empty: no assets yet; explain the import path and next setup/camera action.
  - Loading: synchronizing local metadata and OneDrive/project records.
  - Error: missing cloud asset, analysis failed, metadata conflict, permission-limited OneDrive access.
  - Success: assets and projects are searchable and open into Analysis or Editing.
- **Keyboard/accessibility notes:** Filters and table are keyboard reachable; status badges include labels; row details can be expanded without mouse-only affordances.
- **Data/service dependencies:** `ProjectLibraryService`, `OneDriveService`, `ContentUnderstandingService`, `EditingService`.

## 5. Analysis studio

- **Purpose:** Inspect Azure Content Understanding output and convert explainable timecoded findings into editable decisions.
- **Layout regions:**
  - Preview/player area with current timecode and source asset reference.
  - Analysis timeline with scenes, transcript segments, highlights, and suggestion markers sharing timecodes.
  - Transcript/highlight list with filters and confidence/explanation metadata.
  - Details panel for selected scene/segment/suggestion and edit-draft actions.
- **Primary actions:** Submit/retry analysis, jump to timecode, mark/reject highlight, promote to edit draft, copy/export structured metadata when available.
- **Key states:**
  - Empty: uploaded asset has no analysis; show submit action and service-limit requirements.
  - Loading: analysis queued/running with Azure job status and staging status if Blob is required.
  - Error: unsupported format/size/duration, inaccessible source URL, Azure failure, staging cleanup failure; show retry or settings action.
  - Success: scenes, transcript, highlights, and suggestions are inspectable with source references.
- **Keyboard/accessibility notes:** Player controls, timeline markers, transcript rows, and highlight actions are keyboard operable; timecodes use tabular numbers and copyable text; score is not the only explanation.
- **Data/service dependencies:** `ContentUnderstandingService`, `OneDriveService`, optional Azure Blob staging, `ProjectLibraryService`, `EditingService`.

## 6. Editing studio

- **Purpose:** Build and render non-destructive edit projects from cloud assets, analysis highlights, and user decisions.
- **Layout regions:**
  - Top toolbar: save, undo/redo when implemented, render preset, output target, render action.
  - Preview area with selected source/render preview state.
  - Timeline: tracks, ordered `ClipSegment` blocks, in/out trims, transitions, captions/titles, audio basics, time ruler.
  - Properties panel: selected segment/source reference, trim values, transition/text/audio settings, warnings.
  - Render panel/strip: FFmpeg operation, percent when available, elapsed time, logs, cancellation, output path, OneDrive upload state.
- **Primary actions:** Add/promote highlights, trim, reorder, delete, adjust transition/title/audio, choose render preset, render/export, upload render to OneDrive.
- **Key states:**
  - Empty: no edit project or no segments; offer create from analysis highlights or add source asset.
  - Loading: opening project, generating preview/proxy, probing source, preparing render.
  - Error: missing source asset, invalid timecode, FFmpeg missing/failed, render cancelled, OneDrive render upload failed.
  - Success: edit decision list saved, preview available, final render produced as a new asset.
- **Keyboard/accessibility notes:** Timeline segments use roving tabindex; trim handles have keyboard alternatives; selected state is visible without color alone; render logs are readable and not hidden behind modals.
- **Data/service dependencies:** `EditingService`, `VideoProcessingService`, `ProjectLibraryService`, `OneDriveService`, analysis metadata from `ContentUnderstandingService`.
