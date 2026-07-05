# Azure Media Service

A stateless Go HTTP service that runs in Azure Container Apps and acts as
the intermediary between the AI Video Studio desktop app and Azure Blob
Storage. It streams media from OneDrive directly into Blob Storage
in-memory — no local disk writes — so the desktop app never needs direct
Storage credentials and never has to hold a full copy of the video.

## Why this service exists

- The desktop app authenticates the user against Microsoft Graph
  (delegated OneDrive access) but should not hold Azure Storage account
  keys or long-lived Storage RBAC roles.
- This service holds the Storage identity (via Managed Identity) and
  exposes a small, API-key-gated HTTP API that the desktop app calls to
  stage a OneDrive item into Blob Storage, and to clean it up afterwards.
- Blob Storage is the staging area Azure Content Understanding reads from
  (via a short-lived SAS URL), since Content Understanding needs an
  accessible HTTPS source.

## API

### `GET /health`
No authentication required. Used for Container Apps liveness/readiness probes.

```
200 OK
{"status": "ok"}
```

### `POST /api/v1/copy`
Downloads a OneDrive item and streams it into Blob Storage, returning a
canonical blob URL and a 2-hour read-only SAS URL.

```
Authorization: Bearer <API_KEY>
Content-Type: application/json

{
  "oneDriveItemID": "01ABCDEF...",
  "oneDriveToken":  "eyJ...",
  "blobName":        "analysis/abc123.mp4",
  "blobContainer":   "media-staging"
}
```

```
200 OK
{
  "blobUrl": "https://<account>.blob.core.windows.net/media-staging/analysis/abc123.mp4",
  "sasUrl":  "https://<account>.blob.core.windows.net/media-staging/analysis/abc123.mp4?sv=...&sig=..."
}
```

### `DELETE /api/v1/blobs/{name}`
Deletes a previously staged blob. Uses the default container (or override via
`?container=...`). No request body is needed.

```
Authorization: ******

DELETE /api/v1/blobs/analysis/abc123.mp4?container=media-staging
```

```
200 OK
{"status": "deleted"}
```

> The legacy `POST /api/v1/delete` (JSON body) is kept for backward
> compatibility but may be removed in a future version. New callers should
> use `DELETE /api/v1/blobs/{name}`.


## Configuration

All configuration is via environment variables (see `.env.example`):

| Variable               | Required | Default          | Description                                   |
|-------------------------|----------|------------------|------------------------------------------------|
| `API_KEY`               | yes      | —                | Shared secret desktop clients send as a Bearer token |
| `STORAGE_ACCOUNT_NAME`  | yes      | —                | Azure Storage account name (no domain suffix) |
| `CONTAINER_NAME`        | no       | `media-staging`  | Default blob container                        |
| `PORT`                  | no       | `8080`           | HTTP listen port                              |

Azure Storage authentication uses `azidentity.DefaultAzureCredential`, which
resolves to the Container App's Managed Identity in Azure and to your local
`az login` session (or service principal env vars) during local development.
**No storage account key is ever read from configuration.**

The Managed Identity needs at minimum the **Storage Blob Data Contributor**
role on the target storage account (to upload/delete blobs) — user
delegation SAS generation requires no extra role beyond this, since it
signs with a delegation key obtained from Azure AD via the identity itself.

## Build

```bash
cd azure-media-service
go build ./...
```

## Run locally

```bash
cp .env.example .env
# edit .env with real values, then:
az login   # so DefaultAzureCredential can resolve your identity
$env:API_KEY = "<value>"; $env:STORAGE_ACCOUNT_NAME = "<value>"
go run .
```

Then:

```bash
curl http://localhost:8080/health
```

## Docker

```bash
docker build -t azure-media-service:latest .
docker run -p 8080:8080 \
  -e API_KEY=... \
  -e STORAGE_ACCOUNT_NAME=... \
  azure-media-service:latest
```

The runtime image is `gcr.io/distroless/static-debian12:nonroot` — no
shell, no package manager. When running outside Azure with Managed
Identity unavailable, mount/inject the credentials `DefaultAzureCredential`
expects (e.g. `AZURE_CLIENT_ID` / `AZURE_TENANT_ID` / `AZURE_CLIENT_SECRET`
for a service principal).

## Deploy to Azure Container Apps

```bash
# Build and push the image to a registry the Container App can pull from.
az acr build --registry <your-acr> --image azure-media-service:latest .

# Create/update the Container App, assigning a system-assigned managed identity.
az containerapp create \
  --name azure-media-service \
  --resource-group <rg> \
  --environment <container-apps-env> \
  --image <your-acr>.azurecr.io/azure-media-service:latest \
  --target-port 8080 \
  --ingress external \
  --system-assigned \
  --env-vars API_KEY=secretref:api-key STORAGE_ACCOUNT_NAME=<storage-account> \
  --secrets api-key=<your-api-key>

# Grant the Container App's managed identity Storage access.
principalId=$(az containerapp show -n azure-media-service -g <rg> --query identity.principalId -o tsv)
az role assignment create \
  --assignee "$principalId" \
  --role "Storage Blob Data Contributor" \
  --scope $(az storage account show -n <storage-account> -g <rg> --query id -o tsv)
```

Point Container Apps health probes at `GET /health`.

## Design notes

- **No local disk usage.** `handler.go` streams the OneDrive response body
  (`io.ReadCloser`) directly into `azblob.Client.UploadStream`, which reads
  from the stream in bounded chunks — the full file is never buffered on
  disk or held entirely in memory.
- **Stateless.** No sessions, no local cache, no in-memory state beyond a
  single request's lifetime. Any replica can serve any request.
- **`context.Context` propagation.** Every network call (OneDrive download,
  blob upload/delete, SAS generation) takes the request's context so
  client cancellation/timeouts propagate correctly.
- **Clean error responses.** Handlers never return raw Go errors or stack
  traces to clients — only short, safe messages — full details are logged
  server-side via `log/slog`.
