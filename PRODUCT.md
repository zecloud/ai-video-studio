# AI Video Studio product foundation

## Product register

| Field | Decision |
| --- | --- |
| Product name | AI Video Studio |
| Register | Product/tool |
| Primary device | DJI Osmo Action 4 |
| Primary cloud destination | OneDrive 365 through Microsoft Graph |
| Primary AI service | Azure Content Understanding |
| Desktop stack | Wails v3, Go backend, TypeScript/Vite frontend |
| Workflow type | Import, secure, analyze, prepare, edit, and export action-camera footage |

AI Video Studio is a desktop workflow tool for moving large Osmo Action 4 recordings into OneDrive, analyzing them with Azure AI, and turning the results into non-destructive edit projects. It is not a marketing site, consumer social editor, or generic cloud storage browser.

## Target users

- Solo creators and athletes who record long sessions on an Osmo Action 4 and need to secure footage quickly after capture.
- Video editors who want structured scene, transcript, highlight, and timecode metadata before manual editing.
- Technical creators who are comfortable connecting devices, cloud accounts, and diagnostics when hardware or network behavior is unreliable.
- Small teams that use Microsoft 365 and want originals stored in OneDrive rather than scattered across local machines.

## Job to be done

When I return from a shoot with many large Osmo Action 4 clips, I want to browse the camera, select the footage worth keeping, stream originals directly to OneDrive, run Azure analysis, and create a draft edit from highlights without filling my local disk or losing track of what transferred.

## Core workflow

1. Configure Microsoft 365, Azure Content Understanding, and non-sensitive app settings.
2. Connect to the Osmo Action 4 using BLE/Wi-Fi guidance and diagnostics.
3. Browse camera media with file metadata, storage location, and transfer readiness.
4. Select one or more clips and confirm the OneDrive app-folder destination.
5. Stream camera ranges/chunks directly to Microsoft Graph upload sessions.
6. Track progress, speed, retry state, cancellation, and recoverable errors.
7. Register uploaded assets in the project library.
8. Submit assets to Azure Content Understanding, using Azure Blob staging only when required.
9. Inspect scenes, transcripts, highlights, and edit suggestions.
10. Build a non-destructive edit timeline, render/export a new asset, and optionally upload the render to OneDrive.

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

### DJI Osmo Action 4

- Treat media endpoints and protocol behavior as unconfirmed until tested on real hardware.
- Expected media work includes BLE pairing, Wi-Fi/AP setup, HTTP endpoint validation, `HEAD`, `GET`, `Range`, storage IDs, file paths, thumbnails, and reconnect behavior.
- Keep DJI protocol code behind replaceable Go interfaces.
- Use licensed references carefully: CC0 and MIT projects may inform implementation with attribution; unlicensed references are protocol inspiration only, not copy sources.

### FFmpeg and editing

- Editing is non-destructive. Edit projects store source asset references, timecodes, order, transitions, captions, audio choices, and render presets.
- FFmpeg runtime detection, packaging, codec support, logs, cancellation, and LGPL/GPL implications must be documented before distribution.
- Final renders are new assets. They may be local exports, OneDrive uploads, or both depending on user choice.
