# Azure preparation plan: DTS FFmpeg rendering

## Status

Awaiting approval

## Mode and recipe

- **Mode:** Modify the existing Azure Container Apps deployment.
- **Recipe:** Existing Bicep deployment with Azure Developer CLI-compatible configuration.
- **Deployment:** Not requested. This work prepares and validates artifacts only.

## Objectives

1. Move synchronous FFmpeg work out of `azure-media-service`.
2. Add authenticated asynchronous render-job APIs to `azure-video-indexer-service`.
3. Stage OneDrive inputs in Blob Storage before DTS scheduling, so delegated OneDrive tokens never enter durable job documents or orchestration history.
4. Run rendering only in a dedicated FFmpeg worker Container App using DTS orchestration and activities.
5. Stream rendered output to Blob Storage and return a short-lived output reference for the desktop to publish to OneDrive with its delegated token.

## Planned architecture

| Component | Responsibility | Azure resources |
| --- | --- | --- |
| Video Indexer API | Validate render requests, stage input clips, schedule/query/cancel jobs | Existing API Container App, Blob Storage, Durable Task Scheduler |
| Render job store | Persist render documents separately from Video Indexer jobs | Existing Blob Storage, dedicated `render-jobs/` prefix |
| FFmpeg worker | Download staged inputs to an isolated temporary workspace, run safe FFmpeg arguments, stream result to Blob, and clean up | New private Container App, dedicated managed identity, ACR image |
| DTS | Coordinate validation, rendering, status updates, cancellation, failure compensation, and cleanup | Existing Durable Task Scheduler and task hub |
| Desktop client | Poll render jobs and upload the output Blob to OneDrive with the interactive user's token | Existing Wails desktop application |

## Security and operational decisions

- The worker receives only Blob references; it never receives or persists delegated Microsoft Graph credentials.
- The worker uses managed identity and least-privilege Blob/DTS roles.
- The worker Container App has no public ingress.
- FFmpeg inputs, outputs, and temporary files are scoped to a per-job directory and removed after every terminal path.
- Render requests allow only supported presets and transition modes; unsupported transitions are rejected instead of silently changed.
- Output filenames are normalized before use in storage or local paths.
- FFmpeg logs are bounded and do not include credentials or SAS query strings.

## Planned changes

1. Add render domain documents, Blob store, HTTP endpoints, and API-side staging to `azure-video-indexer-service`.
2. Add render-specific DTS orchestrations, activities, cancellation, Blob streaming helpers, and FFmpeg worker entrypoint.
3. Add a dedicated FFmpeg worker Docker image and Container App, identity, RBAC, DTS scale rules, settings, and deployment workflow image build.
4. Replace the desktop's synchronous media-service render client with an asynchronous Video Indexer render-job client.
5. Remove the media-service render endpoint and its FFmpeg dependency while retaining staging and analysis behavior.
6. Update FFmpeg packaging documentation and run targeted Go, frontend, and Bicep validation. No deployment will be performed.

## Azure context

The subscription and deployment region remain to be confirmed. No Azure deployment or destructive operation is planned by this change.

## Validation

- Unit tests for request validation, job transitions, cancellation, and FFmpeg argument construction.
- Service-module Go tests and builds.
- Root Go tests and frontend checks if the desktop integration changes.
- Bicep deployment validation only after Azure context is confirmed; no deployment execution.
