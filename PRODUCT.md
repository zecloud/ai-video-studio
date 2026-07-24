# AI Video Studio product foundation

## Permanent product split

This repository documents **AI Video Studio**, one of two independent desktop products. The split is permanent and is a product boundary, not a deployment detail:

- **AI Video Camera** owns the Osmo Action hardware workflow only: BLE scan and pairing, Wi-Fi/AP setup, DUML/UDP diagnostics, and HTTP media-endpoint probes and diagnostics. It does not own OneDrive or Azure authentication, cloud credentials, a library, analysis, editing, rendering, or any transfer.
- **AI Video Studio** owns every transfer and every cloud/editor workflow: Camera -> OneDrive and local -> OneDrive transfer, OneDrive authentication, cloud library, Azure Content Understanding, Video Indexer Studio, editing, FFmpeg/renders, and settings.

There is no inter-app transfer, sync, RPC, socket, or shared-file bridge in this product design, now or in the future. The products must not be described as a coordinated pair that hands work to the other app; each product has its own independent UX and responsibilities.

## Product register

| Field | Decision |
| --- | --- |
| Product name | AI Video Studio |
| Register | Product/tool |
| Primary source | Camera-originated media supplied to AI Video Studio, or local media selected by the user |
| Primary cloud destination | OneDrive 365 through Microsoft Graph |
| Primary AI service | Azure Content Understanding |
| Desktop stack | Wails v3, Go backend, TypeScript/Vite frontend |
| Workflow type | Transfer, secure, analyze, prepare, edit, and export media |

AI Video Studio is the cloud and post-production desktop workflow tool for moving media into OneDrive, analyzing it with Azure AI, and turning the results into non-destructive edit projects. Camera hardware control belongs exclusively to AI Video Camera; Studio does not scan, pair, configure, or diagnose the camera. Studio is not a marketing site, consumer social editor, or generic cloud storage browser.

## Target users

- Solo creators and athletes who record long sessions on an Osmo Action 4 and need to secure footage quickly after capture.
- Video editors who want structured scene, transcript, highlight, and timecode metadata before manual editing.
- Technical creators who are comfortable connecting devices, cloud accounts, and diagnostics when hardware or network behavior is unreliable.
- Small teams that use Microsoft 365 and want originals stored in OneDrive rather than scattered across local machines.

## Job to be done

When I have media ready for Studio, I want to transfer it directly to OneDrive, run Azure analysis, and create a draft edit from highlights without filling my local disk or losing track of what transferred. Camera discovery and hardware diagnostics are a separate AI Video Camera workflow and are not part of this job.

## Core workflow

1. Configure Microsoft 365, Azure services, and non-sensitive Studio settings.
2. Select media from an available source, including camera-originated media made available to Studio or local media.
3. Confirm the OneDrive app-folder destination and transfer policy.
4. Stream camera-originated ranges/chunks, or local input, into Microsoft Graph upload sessions without persisting a complete camera original locally.
5. Track progress, speed, retry state, cancellation, and recoverable errors.
6. Register uploaded assets in the project library.
7. Submit assets to Azure Content Understanding, using Azure Blob staging only when required.
8. Inspect scenes, transcripts, highlights, Video Indexer Studio results, and edit suggestions.
9. Build a non-destructive edit timeline, render/export a new asset, and optionally upload the render to OneDrive.

## Product personality

- Calm: show real state and next actions instead of hype.
- Precise: use clear labels for camera, cloud, transfer, analysis, and render states.
- Dense but readable: prioritize tables, split panes, queues, and timelines over decorative cards.
- Trustworthy: make storage, privacy, retries, and failures visible.
- Recoverable: every failure state should explain what happened and what the user can try next.

## Non-goals

- Replacing a full professional NLE.
- Editing or modifying original camera files in place.
- Persisting full original video files locally during ingestion.
- Becoming a general-purpose OneDrive file manager.
- Depending on unofficial DJI behavior without diagnostics and replaceable adapters.
- Owning camera discovery, pairing, Wi-Fi/AP setup, DUML/UDP, or camera endpoint diagnostics (these belong to AI Video Camera).
- Implementing any inter-app bridge, sync, RPC, socket, or shared-file handoff with AI Video Camera.
- Shipping embedded secrets, tenant secrets, client secrets, SAS URLs, or generated caches.
- Using Azure AI to make opaque edit decisions without editable timecodes and source references.

## Anti-references

Avoid these patterns:

- Landing-page layouts with hero metrics, decorative gradients, or generic feature cards.
- Glassmorphism, blurred chrome, tiny uppercase eyebrow labels, and low-contrast muted text.
- Modal-heavy flows that hide diagnostics or block long-running operations.
- Cheerful success-shaped defaults when camera, Graph, Azure, or FFmpeg state is unknown.
- Dark-pattern permission prompts that request broad Microsoft Graph access before a clear product need.
- Local import flows that silently write complete original videos to disk.

## Principles

### Accessibility

- Body text contrast must be at least 4.5:1; large text must be at least 3:1.
- Every interactive control needs keyboard access, visible focus, and disabled/loading/error states.
- Progress must not rely on color alone; include labels, percentages, byte counts, or status text.
- Respect `prefers-reduced-motion`.
- Prefer inline help, persistent diagnostics, and accessible tables over tooltips as the only source of meaning.

### Privacy and security

- Use least-privilege Microsoft Graph permissions. Start with `Files.ReadWrite.AppFolder`; broaden only with an explicit product reason.
- Treat desktop auth as a public-client flow using PKCE or device code. Never embed a client secret.
- Store only non-sensitive preferences in normal app settings.
- Use OS-appropriate secure storage for tokens if persistent token cache is needed.
- Do not commit credentials, tokens, SAS URLs, tenant secrets, generated local caches, or private media.
- If Azure Blob staging is required for analysis, use short-lived access and explicit retention/deletion.

### Zero full local original storage

Original camera videos must not be persisted as complete local files during ingestion.

Allowed local persistence:

- Non-sensitive settings.
- Transfer metadata, resumable state, and logs.
- Thumbnails, short proxies, analysis metadata, and edit decision lists.
- Explicit user-requested renders/exports.

Required ingestion behavior:

- Read camera media as bounded memory chunks or HTTP ranges.
- Upload chunks directly to Microsoft Graph upload sessions.
- Keep memory bounded and observable.
- Make any editing/export temporary files visible as editing/export behavior, not import behavior.

## Platform constraints

### OneDrive and Microsoft Graph

- Prefer the OneDrive app folder and delegated least-privilege access.
- Use Graph upload sessions for large files.
- Model `Content-Range`, resumable upload state, retry, token expiration, throttling, and cancellation explicitly.
- Surface permission scope and destination folder clearly in onboarding and settings.

### Azure Content Understanding

- Assume service limits for video format, file size, duration, and source URL must be validated.
- If OneDrive URLs are not accepted directly, stage through Azure Blob with short-lived SAS and a deletion policy.
- Normalize results into editing-oriented models: scenes, transcript segments, highlight candidates, and edit suggestions.
- Keep AI output inspectable and editable; timecodes and source asset references are mandatory.

### DJI Osmo Action 4 boundary

AI Video Camera owns all Osmo hardware and camera-endpoint work: BLE scan/pairing, Wi-Fi/AP setup, DUML/UDP, HTTP `HEAD`/`GET`/`Range` probes, storage IDs, paths, thumbnails, reconnect behavior, and diagnostics. AI Video Studio does not contain or invoke that workflow. There is no designed mechanism for Camera to transfer or synchronize media with Studio; Studio's transfer inputs are treated as independently available media sources.

Treat camera behavior as unconfirmed until tested on real hardware, keep protocol code behind replaceable Go interfaces in the Camera product, and use licensed references carefully: CC0 and MIT projects may inform implementation with attribution; unlicensed references are protocol inspiration only, not copy sources.

### FFmpeg and editing

- Editing is non-destructive. Edit projects store source asset references, timecodes, order, transitions, captions, audio choices, and render presets.
- FFmpeg runtime detection, packaging, codec support, logs, cancellation, and LGPL/GPL implications must be documented before distribution.
- Final renders are new assets. They may be local exports, OneDrive uploads, or both depending on user choice.
