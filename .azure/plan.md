# Azure preparation plan: DTS FFmpeg rendering

## Status

Ready for validation

## Mode and recipe

- **Mode:** Modify the existing Azure Container Apps deployment.
- **Recipe:** Existing Bicep deployment with Azure Developer CLI-compatible configuration.
- **Deployment:** Not requested. This work prepares and validates artifacts only.

## Objectives

1. Move synchronous FFmpeg work out of `azure-media-service`.
2. Add authenticated asynchronous render-job APIs to `azure-video-indexer-service`.
3. Stage OneDrive inputs in Blob Storage before DTS scheduling, so delegated OneDrive tokens never enter durable job documents or orchestration history.
4. Run rendering only in a dedicated FFmpeg worker Container App using DTS orchestration and activities.
5. Stream rendered output to Blob Storage and proxy it through the authenticated API for the desktop to publish to OneDrive with its delegated token; no output reference or SAS URL is exposed.

## Planned architecture

| Component | Responsibility | Azure resources |
| --- | --- | --- |
| Video Indexer API | Validate render requests, stage input clips, schedule/query/cancel jobs | Existing API Container App, Blob Storage, Durable Task Scheduler |
| Render job store | Persist render documents separately from Video Indexer jobs | Existing Blob Storage, dedicated `render-jobs/` prefix |
| FFmpeg worker | Download staged inputs to an isolated temporary workspace, run safe FFmpeg arguments, stream result to Blob, and clean up | New private Container App, dedicated managed identity, dedicated ACR image |
| DTS | Coordinate validation, rendering, status updates, cancellation, failure compensation, and cleanup | Existing Durable Task Scheduler with a dedicated `ffmpeg-render` task hub |
| Desktop client | Poll render jobs and upload the output Blob to OneDrive with the interactive user's token | Existing Wails desktop application |

## Security and operational decisions

- The worker receives only Blob references; it never receives or persists delegated Microsoft Graph credentials.
- The worker uses managed identity and least-privilege Blob/DTS roles.
- The worker Container App has no public ingress.
- FFmpeg inputs and temporary files are scoped to a per-job directory and removed after every terminal path. Successful Blob outputs are available only through the authenticated API and expire through a seven-day Blob lifecycle policy.
- Render requests allow only supported presets and transition modes; unsupported transitions are rejected instead of silently changed.
- Output filenames are normalized before use in storage or local paths.
- FFmpeg logs are bounded and do not include credentials or SAS query strings.
- **Azure context:** Microsoft Azure Sponsorship (`5459e31a-a44f-4526-847e-73352604bc98`), `swedencentral`.
- **FFmpeg worker sizing:** 2 vCPU and 4 GiB memory per replica.
- **DTS scaling:** scale to zero, maximum 3 replicas, and one FFmpeg activity per replica.
- **Temporary workspace:** Container Apps `EmptyDir` mounted at `/render-work`. At 2 vCPU, its 8 GiB platform limit requires a documented 6 GiB maximum total working set per render. API-side admission enforcement is a follow-up; this infrastructure alone cannot reject oversized render requests.
- **Render timeout:** 2 hours (`RENDER_TIMEOUT=2h`).
- **Image and licensing:** a dedicated ACR repository is built from an Alpine image with the distribution `ffmpeg` package. The renderer uses FFmpeg's native MPEG-4 encoder, not GPL `libx264` or `libx265`; applicable dynamically-linked package and FFmpeg notices must ship with the image.
- **RBAC:** the FFmpeg UAMI receives only AcrPull on the ACR, Storage Blob Data Contributor on the staging/jobs storage account, and Durable Task Data Contributor on the scheduler.

## Planned changes

1. Add render domain documents, Blob store, HTTP endpoints, and API-side staging to `azure-video-indexer-service`.
2. Add render-specific DTS orchestrations, activities, cancellation, Blob streaming helpers, and FFmpeg worker entrypoint.
3. Add a dedicated FFmpeg worker Docker image and Container App, identity, RBAC, DTS scale rules, settings, and deployment workflow image build.
4. Replace the desktop's synchronous media-service render client with an asynchronous Video Indexer render-job client, returning an actionable configuration error when it is unavailable.
5. Retain the legacy media-service endpoint only for separately deployed backward compatibility; the desktop does not fall back to it.
6. Update FFmpeg packaging documentation and run targeted Go and Bicep validation. No deployment will be performed.

## Azure context

Subscription: Microsoft Azure Sponsorship (`5459e31a-a44f-4526-847e-73352604bc98`).

Location: `swedencentral`.

Capacity checked with Azure CLI on 2026-07-15: Container Apps managed environments use 3 of 50 (one existing environment is reused); Storage accounts use 2 of 250 (one new account is planned). No Azure deployment or destructive operation is planned by this change.

## Validation

- Unit tests for request validation, job transitions, cancellation, and FFmpeg argument construction.
- Service-module Go tests and builds.
- Root Go tests and frontend checks if the desktop integration changes.
- Bicep compile locally; Azure resource-group validation remains a CI/deployment workflow action and is not executed locally.
- Docker image build when a local Docker daemon is available; otherwise, CI/ACR performs the image build.

## Validation proof

| Check | Command | Result |
| --- | --- | --- |
| Azure account | `az account show --query '{subscription:name,id:id,tenantId:tenantId}' --output json` | Passed: Microsoft Azure Sponsorship (`5459e31a-a44f-4526-847e-73352604bc98`) is active. |
| Bicep compilation | `az bicep build --file infra/main.bicep --stdout` | Passed. The existing unused `serviceName` variable remains a warning. |
| Bicep lint | `az bicep lint --file infra/main.bicep` | Passed with the same existing unused-variable warning. |
| Service tests | `go test ./...` from `azure-video-indexer-service` | Passed. |
| Change formatting | `git diff --check` | Passed. |
| Worker image build | `docker version --format '{{.Server.Version}}'` | Blocked: the local Docker daemon is not running. The deployment workflow builds this image with ACR. |
| Azure template validation and what-if | `az deployment group validate` / `az deployment group what-if` | Not run: this worktree has no target resource group, Container Apps environment ID, or deployment API secret. The existing CI workflow supplies them and validates before deployment. |

The plan is not marked `Validated`: full Azure template validation, what-if, and Docker image build remain intentionally unexecuted pending deployment inputs and a running Docker daemon. No Azure resource deployment was performed.
