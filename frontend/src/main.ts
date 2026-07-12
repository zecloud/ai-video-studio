import "./styles.css";

import { Dialogs } from "@wailsio/runtime";
import {
  AppService,
  CameraService,
  ContentUnderstandingService,
  DJIControlService,
  FileDialogService,
  OneDriveService,
  ProjectLibraryService,
  SettingsService,
  TransferService,
  VideoProcessingService,
} from "../bindings/github.com/zecloud/ai-video-studio/internal/backend/index.js";
import {
  BLEDevice,
  ControlStatus,
  DiagnosticResult,
  PairingRequest,
  ProtocolProfile,
} from "../bindings/github.com/zecloud/ai-video-studio/internal/dji/models.js";
import { ProjectAsset } from "../bindings/github.com/zecloud/ai-video-studio/internal/library/models.js";
import { AppSettings } from "../bindings/github.com/zecloud/ai-video-studio/internal/settings/models.js";
import { AuthFlow } from "../bindings/github.com/zecloud/ai-video-studio/internal/onedrive/models.js";
import {
  DescribeLocalFilesRequest,
  LocalMediaFile,
  LocalToOneDriveRequest,
  StartTransferRequest,
  TransferJob,
} from "../bindings/github.com/zecloud/ai-video-studio/internal/transfer/models.js";
import {
  createVideoIndexerStudioState,
  createEditProjectFromVideoIndexerJob,
  cancelVideoIndexerJob,
  loadVideoIndexerStudioState,
  renderVideoIndexerSettingsCard,
  renderVideoIndexerStudioPanel,
  saveVideoIndexerSettings,
  selectVideoIndexerJob,
  setupVideoIndexerStudioEvents,
  submitVideoIndexerAssets,
  refreshVideoIndexerStudioState,
  toggleVideoIndexerAssetSelection,
  VideoIndexerStudioViewState,
} from "./video-indexer-studio.js";
import {
  AnalysisViewState,
  createAnalysisState,
  refreshAnalysisState,
  renderAnalysisPanel,
  setupAnalysisEvents,
  submitAssetForAnalysis,
  submitPendingAssets,
} from "./analysis.js";
import {
  EditingViewState,
  createEditingState,
  loadEditingData,
  renderEditingPanel,
  setupEditingEvents,
  createNewProject,
  saveProject,
  addClipToTimeline,
  removeClip,
  moveClipUp,
  moveClipDown,
  startRender,
} from "./editing.js";

type Tone = "success" | "warning" | "danger" | "info" | "neutral";
type AppView = "camera" | "transfers" | "library" | "analysis" | "smart-edit" | "editing" | "settings";

type StatusItem = {
  label: string;
  value: string;
  tone: Tone;
};

type MediaItem = {
  id: string;
  name: string;
  capturedAt: string;
  duration: string;
  size: string;
  storage: string;
  selected: boolean;
  readiness: string;
  tone: Tone;
};

type QueueItem = {
  name: string;
  detail: string;
  progress: number;
  metric: string;
  status: string;
  tone: Tone;
};

type LocalMediaItem = LocalMediaFile & {
  selected: boolean;
  readiness: string;
  tone: Tone;
};

type DiagnosticItem = {
  label: string;
  detail: string;
  tone: Tone;
};

type AppState = {
  title: string;
  version: string;
  description: string;
  status: StatusItem[];
  activeView: AppView;
  media: MediaItem[];
  localMedia: LocalMediaItem[];
  queue: QueueItem[];
  diagnostics: DiagnosticItem[];
  transferMessage: string;
  settings: AppSettings | null;
  authChallenge: {
    userCode: string;
    verificationUri: string;
    message: string;
    expiresAt: string;
    intervalSeconds: number;
  } | null;
  djiStatus: ControlStatus | null;
  djiProtocol: ProtocolProfile | null;
  djiDiagnostics: DiagnosticResult | null;
  bleDevices: BLEDevice[];
  selectedBleDeviceId: string;
  bleActionMessage: string;
  settingsMessage: string;
  localTransferInFlight: boolean;
    libraryAssets: ProjectAsset[];
    selectedAssetId: string | null;
    loading: boolean;
    analysis: AnalysisViewState;
    smartEdit: VideoIndexerStudioViewState;
        editing: EditingViewState;
    };

const sampleMedia: MediaItem[] = [
  {
    id: "gx010112",
    name: "GX010112.MP4",
    capturedAt: "2026-07-04 09:42",
    duration: "08:14",
    size: "3.8 GB",
    storage: "SD / DCIM",
    selected: true,
    readiness: "Range OK",
    tone: "success",
  },
  {
    id: "gx010113",
    name: "GX010113.MP4",
    capturedAt: "2026-07-04 10:05",
    duration: "12:32",
    size: "5.6 GB",
    storage: "SD / DCIM",
    selected: true,
    readiness: "HEAD pending",
    tone: "warning",
  },
  {
    id: "gx010114",
    name: "GX010114.MP4",
    capturedAt: "2026-07-04 10:49",
    duration: "03:58",
    size: "1.9 GB",
    storage: "SD / DCIM",
    selected: false,
    readiness: "Already imported",
    tone: "info",
  },
];

const state: AppState = {
  title: "AI Video Studio",
  version: "0.1.0-scaffold",
  description:
    "Desktop workflow for DJI Osmo Action 4 import, OneDrive transfer, Azure Content Understanding, and non-destructive editing.",
  status: [
    { label: "Camera", value: "Loading status", tone: "neutral" },
    { label: "OneDrive", value: "Loading status", tone: "neutral" },
    { label: "Azure CU", value: "Loading status", tone: "neutral" },
    { label: "FFmpeg", value: "Loading status", tone: "neutral" },
    { label: "Active work", value: "0 transfers, 0 renders", tone: "neutral" },
  ],
  activeView: "transfers",
  media: sampleMedia,
  localMedia: [],
  queue: [],
  diagnostics: [],
  transferMessage: "No transfer jobs yet. Choose local Osmo videos to start a OneDrive upload.",
  settings: null,
  authChallenge: null,
  djiStatus: null,
  djiProtocol: null,
  djiDiagnostics: null,
  bleDevices: [],
  selectedBleDeviceId: "",
  bleActionMessage: "Scan Windows BLE to find nearby Osmo/DJI candidates, then run the GATT readiness pair test.",
  settingsMessage: "Configure Microsoft Entra public-client details, then start device-code sign-in.",
  localTransferInFlight: false,
    libraryAssets: [],
    selectedAssetId: null,
  loading: true,
  analysis: createAnalysisState(),
    smartEdit: createVideoIndexerStudioState(),
    editing: createEditingState(),
  };

const app = document.querySelector<HTMLDivElement>("#app");

if (!app) {
  throw new Error("App root was not found");
}

const root = app;
let transferQueuePollID: number | undefined;
let transferQueueRefreshInFlight = false;
let mainDelegatedListenersInitialized = false;

function escapeHTML(value: string): string {
  return value.replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;").replaceAll('"', "&quot;");
}

function renderBadge(label: string, tone: Tone): string {
  return `<span class="badge ${tone}">${escapeHTML(label)}</span>`;
}

function renderProgress(value: number, label: string): string {
  const bounded = Math.max(0, Math.min(100, value));
  return `
    <div class="progress">
      <div class="bar" aria-hidden="true"><span style="width: ${bounded}%"></span></div>
      <span>${escapeHTML(label)}</span>
    </div>
  `;
}

function diagnosticTone(status: string): Tone {
  switch (status) {
    case "available":
      return "info";
    case "blocked":
      return "danger";
    case "pending":
      return "warning";
    default:
      return "neutral";
  }
}

function renderProtocolCell(label: string, value: string): string {
  return `
    <div class="protocol-cell">
      <span>${escapeHTML(label)}</span>
      <strong>${escapeHTML(value)}</strong>
    </div>
  `;
}

function renderDJIProtocolPanel(): string {
  const protocol = state.djiProtocol ?? state.djiStatus?.protocol ?? new ProtocolProfile();
  const ble = protocol.ble;
  const steps = state.djiDiagnostics?.steps ?? [];
  const status = state.djiStatus;
  const statusTone: Tone = status?.adapterConfigured ? "success" : "warning";
  const statusLabel = status?.adapterConfigured ? "Adapter configured" : "Adapter not configured";
  const ports = protocol.mediaPorts?.length ? protocol.mediaPorts.join(", ") : "80, 7001";

  return `
    <section class="panel" aria-labelledby="ble-title">
      <div class="panel-header">
        <div>
          <p class="eyebrow">BLE / DUML diagnostics</p>
          <h3 id="ble-title">${escapeHTML(protocol.modelHint || "Osmo Action 4 control boundary")}</h3>
        </div>
        ${renderBadge(statusLabel, statusTone)}
      </div>
      <div class="protocol-grid" aria-label="DJI protocol profile">
        ${renderProtocolCell("GATT service", ble.serviceUuid || "fff0")}
        ${renderProtocolCell("Write / control", ble.writeCharUuid || "fff3")}
        ${renderProtocolCell("Pairing notify", ble.pairingCharUuid || "fff4")}
        ${renderProtocolCell("Status notify", ble.statusCharUuid || "fff5")}
        ${renderProtocolCell("Default PIN candidate", ble.defaultPin || "love")}
        ${renderProtocolCell("Camera gateway", protocol.defaultIp || "192.168.2.1")}
        ${renderProtocolCell("Media ports", ports)}
        ${renderProtocolCell("DUML UDP", protocol.udpPort ? String(protocol.udpPort) : "9004")}
      </div>
      <div class="ble-actions">
        <div>
          <strong>Windows BLE adapter</strong>
          <p>${escapeHTML(state.bleActionMessage)}</p>
        </div>
        <div class="actions">
          <button class="button secondary" type="button" data-action="scan-ble">Scan BLE</button>
          <button class="button" type="button" data-action="pair-ble" ${state.selectedBleDeviceId ? "" : "disabled"}>Pair / GATT test</button>
        </div>
      </div>
      <div class="ble-device-list" aria-label="Scanned BLE devices">
        ${
          state.bleDevices.length
            ? state.bleDevices
                .map((device) => {
                  const selected = device.id === state.selectedBleDeviceId;
                  const label = device.name || "Unnamed BLE peripheral";
                  const serviceText = device.serviceUuids?.length ? device.serviceUuids.join(", ") : "No advertised service UUIDs";
                  return `
                    <button class="ble-device ${selected ? "selected" : ""}" type="button" data-action="select-ble-device" data-device-id="${escapeHTML(device.id)}">
                      <span>${renderBadge(selected ? "Selected" : "BLE", selected ? "success" : "neutral")}</span>
                      <strong>${escapeHTML(label)}</strong>
                      <small>${escapeHTML(device.model || "BLE peripheral")} - RSSI ${device.rssi} - ${escapeHTML(device.address || device.id)}</small>
                      <small>${escapeHTML(serviceText)}</small>
                    </button>
                  `;
                })
                .join("")
            : `<div class="ble-empty">
                <strong>No scan results yet</strong>
                <p>Put the Osmo Action 4 in wireless/app control mode, keep it near the PC, then scan.</p>
              </div>`
        }
      </div>
      <div class="step-list" aria-label="BLE and DUML diagnostic plan">
        ${
          steps.length
            ? steps
                .map(
                  (step) => `
                    <div class="step-row">
                      ${renderBadge(step.status || "pending", diagnosticTone(step.status))}
                      <div>
                        <strong>${escapeHTML(step.label)}</strong>
                        <p>${escapeHTML(step.description)}</p>
                      </div>
                      <span class="muted">${escapeHTML(step.transport || "-")}</span>
                    </div>
                  `,
                )
                .join("")
            : `<div class="step-row">
                ${renderBadge("Pending", "warning")}
                <div>
                  <strong>Diagnostics not loaded</strong>
                  <p>Run diagnostics to load the BLE/DUML profile and safe hardware readiness plan.</p>
                </div>
                <span class="muted">BLE</span>
              </div>`
        }
      </div>
      <div class="detail-body protocol-note">
        <p class="queue-message">${escapeHTML(status?.message || state.djiDiagnostics?.message || protocol.referencePolicy || "No BLE/DUML commands are issued until a hardware adapter is configured.")}</p>
      </div>
    </section>
  `;
}

function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) {
    return "0 B";
  }
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = bytes;
  let unitIndex = 0;
  while (value >= 1024 && unitIndex < units.length - 1) {
    value /= 1024;
    unitIndex += 1;
  }
  return `${value >= 10 || unitIndex === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[unitIndex]}`;
}

function formatDate(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return "-";
  }
  return date.toLocaleString();
}

function selectedLocalFiles(): LocalMediaItem[] {
  return state.localMedia.filter((file) => file.selected);
}

function selectedLocalSizeBytes(): number {
  return selectedLocalFiles().reduce((total, file) => total + file.sizeBytes, 0);
}

function visibleTransferQueue(): QueueItem[] {
  if (state.queue.length > 0) {
    return state.queue;
  }
  if (!state.localTransferInFlight) {
    return [];
  }
  return [
    {
      name: "local-files",
      detail: "Starting OneDrive upload",
      progress: 0,
      metric: "Preparing transfer job...",
      status: "running",
      tone: "info",
    },
  ];
}

function renderLocalMediaTable(): string {
  const selectedCount = selectedLocalFiles().length;
  const uploadDisabled = !selectedCount || state.localTransferInFlight;
  return `
    <section class="panel" aria-labelledby="local-media-title">
      <div class="panel-header">
        <div>
          <p class="eyebrow">USB / local disk source</p>
          <h3 id="local-media-title">Mounted Osmo files</h3>
        </div>
        <div class="toolbar">
          ${renderBadge(`${selectedCount} selected`, selectedCount ? "success" : "neutral")}
          <button class="button secondary" type="button" data-action="choose-local-files">Choose video files</button>
          <button class="button" type="button" data-action="start-local-transfer" ${uploadDisabled ? "disabled" : ""}>${state.localTransferInFlight ? "Uploading..." : "Upload selected"}</button>
        </div>
      </div>
      <div class="local-drop">
        <strong>Select files from the Osmo USB drive or another mounted disk.</strong>
        <p>The app reads each source file in bounded Graph-compatible chunks and does not duplicate the complete original into its own cache.</p>
      </div>
      <div class="table-wrap">
        <table aria-label="Local video files">
          <thead>
            <tr>
              <th><input class="check" type="checkbox" data-action="toggle-local-all" ${selectedCount && selectedCount === state.localMedia.length ? "checked" : ""} aria-label="Select all local media" /></th>
              <th>Name</th>
              <th>Size</th>
              <th>Modified</th>
              <th>Path</th>
              <th>Readiness</th>
            </tr>
          </thead>
          <tbody>
            ${
              state.localMedia.length
                ? state.localMedia
                    .map(
                      (file) => `
                        <tr>
                          <td><input class="check" type="checkbox" data-action="toggle-local-file" data-local-id="${escapeHTML(file.id)}" ${file.selected ? "checked" : ""} aria-label="Select ${escapeHTML(file.name)}" /></td>
                          <td><strong>${escapeHTML(file.name)}</strong><br><span class="muted">${escapeHTML(file.contentType || "video file")}</span></td>
                          <td class="muted">${formatBytes(file.sizeBytes)}</td>
                          <td class="muted">${escapeHTML(formatDate(file.modifiedAt))}</td>
                          <td class="path-cell">${escapeHTML(file.path)}</td>
                          <td>${renderBadge(file.readiness, file.tone)}</td>
                        </tr>
                      `,
                    )
                    .join("")
                : `<tr>
                    <td colspan="6">
                      <div class="empty-state">
                        <strong>No local videos selected yet</strong>
                        <p>Connect the Osmo over USB, choose MP4/MOV/M4V/LRV files from the mounted drive, then upload them to OneDrive.</p>
                      </div>
                    </td>
                  </tr>`
            }
          </tbody>
        </table>
      </div>
    </section>
  `;
}

function renderTransferQueuePanel(title = "Transfer queue", subtitle = "OneDrive upload jobs"): string {
  const queue = visibleTransferQueue();
  return `
    <section class="panel" aria-labelledby="queue-title">
      <div class="panel-header">
        <div>
          <p class="eyebrow">${escapeHTML(title)}</p>
          <h3 id="queue-title">${escapeHTML(subtitle)}</h3>
        </div>
        ${renderBadge("Bounded memory chunks", "info")}
      </div>
      <div class="queue">
        <p class="queue-message">${escapeHTML(state.transferMessage)}</p>
        ${
          queue.length
            ? queue
                .map(
                  (item) => `
                    <div class="queue-row">
                      <div><strong>${escapeHTML(item.name)}</strong><br><span class="muted">${escapeHTML(item.detail)}</span></div>
                      ${renderProgress(item.progress, item.metric)}
                      ${renderBadge(item.status, item.tone)}
                    </div>
                  `,
                )
                .join("")
            : `<div class="empty-state">
                <strong>No transfer jobs yet</strong>
                <p>Choose local Osmo videos or start a camera transfer to populate this queue.</p>
              </div>`
        }
      </div>
    </section>
  `;
}

function renderSettingsPanel(): string {
  const graphSettings = state.settings?.graphAuth;
  const destination = state.settings?.oneDriveDestination;
  return `
    <section class="panel" id="settings" aria-labelledby="settings-title">
      <div class="panel-header">
        <div>
          <p class="eyebrow">OneDrive settings</p>
          <h3 id="settings-title">Microsoft Graph delegated auth</h3>
        </div>
        ${renderBadge(graphSettings?.clientId ? "Public client configured" : "Client ID required", graphSettings?.clientId ? "info" : "warning")}
      </div>
      <div class="detail-body">
        <div class="settings-grid">
          <label>
            <span>Tenant</span>
            <input class="field" data-setting="tenant" value="${escapeHTML(graphSettings?.tenantId || state.settings?.tenantId || "organizations")}" aria-label="Microsoft tenant ID" />
          </label>
          <label>
            <span>Application client ID</span>
            <input class="field" data-setting="client" value="${escapeHTML(graphSettings?.clientId || state.settings?.clientId || "")}" placeholder="00000000-0000-0000-0000-000000000000" aria-label="Microsoft Entra application client ID" />
          </label>
          <label>
            <span>OneDrive folder</span>
            <input class="field" data-setting="folder" value="${escapeHTML(state.settings?.oneDriveFolder || "AI Video Studio")}" aria-label="OneDrive folder name" />
          </label>
          <label>
            <span>Chunk size</span>
            <input class="field" data-setting="chunk" type="number" value="${state.settings?.chunkSizeBytes ?? 10485760}" aria-label="Upload chunk size bytes" />
          </label>
        </div>
        <div class="kv"><span>Scope</span><strong>${escapeHTML((graphSettings?.scopes || ["Files.ReadWrite.AppFolder"]).join(", "))}</strong></div>
        <div class="kv"><span>Destination</span><strong>${escapeHTML(destination?.path || "/Apps/AI Video Studio")}</strong></div>
      </div>
    </section>
    <section class="panel" id="media-service-settings" aria-labelledby="media-service-settings-title">
      <div class="panel-header">
        <div>
          <p class="eyebrow">Media Service</p>
          <h3 id="media-service-settings-title">Media Service connection</h3>
        </div>
        ${renderBadge(state.settings?.mediaServiceEndpoint ? "Endpoint configured" : "Endpoint required", state.settings?.mediaServiceEndpoint ? "info" : "warning")}
      </div>
      <div class="detail-body">
        <div class="settings-grid">
          <label>
            <span>Media Service endpoint</span>
            <input class="field" data-setting="media-endpoint" value="${escapeHTML(state.settings?.mediaServiceEndpoint || "")}" placeholder="https://media-staging.azurecontainerapps.io" aria-label="Media Service endpoint" />
          </label>
          <label>
            <span>Media Service API key</span>
            <input class="field" data-setting="media-apikey" type="password" value="" placeholder="••••" autocomplete="new-password" aria-label="Media Service API key" />
          </label>
        </div>
        <p class="queue-message">The API key is never displayed once saved. Leave it blank to keep the stored key unchanged.</p>
      </div>
    </section>
    ${renderVideoIndexerSettingsCard(state.smartEdit)}
    <section class="panel" id="settings-status" aria-labelledby="settings-status-title">
      <div class="panel-header">
        <div>
          <h3 id="settings-status-title">Sign-in and save</h3>
        </div>
      </div>
      <div class="detail-body">
        <p class="queue-message">${escapeHTML(state.settingsMessage)}</p>
        ${
          state.authChallenge
            ? `<div class="code-card">
                <span>Open ${escapeHTML(state.authChallenge.verificationUri)}</span>
                <strong>${escapeHTML(state.authChallenge.userCode)}</strong>
                <small>${escapeHTML(state.authChallenge.message || "Enter this code in the Microsoft browser sign-in page.")}</small>
              </div>`
            : ""
        }
        <div class="actions">
          <button class="button secondary" type="button" data-action="save-settings">Save settings</button>
          <button class="button" type="button" data-action="start-onedrive-auth">Start OneDrive sign-in</button>
          <button class="button secondary" type="button" data-action="poll-onedrive-auth">Check sign-in</button>
          <button class="button secondary" type="button" data-action="sign-out-onedrive">Sign out</button>
        </div>
      </div>
    </section>
  `;
}

function renderCameraMediaPanel(selectedCount: number): string {
  return `
    <section class="panel" aria-labelledby="media-title">
      <div class="panel-header">
        <div>
          <p class="eyebrow">Camera media browser</p>
          <h3 id="media-title">DCIM/100MEDIA</h3>
        </div>
        <div class="toolbar">
          <input class="field" value="GX01" aria-label="Filter media" />
          <select class="field" aria-label="Storage filter"><option>Storage 1 - SD card</option></select>
          <button class="button secondary" type="button" data-action="refresh">Refresh</button>
        </div>
      </div>
      <div class="table-wrap">
        <table aria-label="Camera media files">
          <thead>
            <tr>
              <th><input class="check" type="checkbox" ${selectedCount === state.media.length ? "checked" : ""} aria-label="Select all media" /></th>
              <th>Preview</th>
              <th>Name</th>
              <th>Duration</th>
              <th>Size</th>
              <th>Storage</th>
              <th>Transfer readiness</th>
            </tr>
          </thead>
          <tbody>
            ${state.media
              .map(
                (item) => `
                  <tr>
                    <td><input class="check" type="checkbox" ${item.selected ? "checked" : ""} aria-label="Select ${escapeHTML(item.name)}" /></td>
                    <td><div class="thumb" aria-hidden="true"></div></td>
                    <td><strong>${escapeHTML(item.name)}</strong><br><span class="muted">${escapeHTML(item.capturedAt)}</span></td>
                    <td class="muted">${escapeHTML(item.duration)}</td>
                    <td class="muted">${escapeHTML(item.size)}</td>
                    <td class="muted">${escapeHTML(item.storage)}</td>
                    <td>${renderBadge(item.readiness, item.tone)}</td>
                  </tr>
                `,
              )
              .join("")}
          </tbody>
        </table>
      </div>
    </section>
  `;
}

function renderEditingPanelWrapper(): string {
  return `
    <section class="panel" aria-labelledby="edit-title">
      <div class="topbar">
        <div>
          <p class="eyebrow">AI Video Studio</p>
          <h2>Editing Studio</h2>
          <p class="lede">Drag analyzed assets into the timeline. Trim, reorder, add transitions, and render via Azure Container App.</p>
        </div>
        <div style="display:flex;gap:10px;align-items:center">
          <button class="button secondary" data-action="save-project">Save</button>
        </div>
      </div>
      ${renderEditingPanel(state.editing)}
    </section>
  `;
}

function renderDetailsPanel(destinationPath: string): string {
  if (state.activeView === "transfers") {
    const selected = selectedLocalFiles();
    const selectedFile = selected.length === 1 ? selected.at(0) : undefined;
    const title =
      selectedFile
        ? selectedFile.name
        : selected.length > 1
          ? `${selected.length} local files selected`
          : "No local media selected";
    return `
      <section class="panel">
        <div class="panel-header">
          <div>
            <p class="eyebrow">Selected local media</p>
            <h3>${escapeHTML(title)}</h3>
          </div>
          ${renderBadge(selected.length ? `${selected.length} selected` : "Empty", selected.length ? "success" : "neutral")}
        </div>
        <div class="detail-body">
          <div class="kv"><span>Source</span><strong>${state.localMedia.length ? "Mounted disk / USB path" : "Choose USB videos first"}</strong></div>
          <div class="kv"><span>Total size</span><strong>${formatBytes(selectedLocalSizeBytes())}</strong></div>
          <div class="kv"><span>Destination</span><strong>${escapeHTML(destinationPath)}</strong></div>
          <div class="kv"><span>Chunking</span><strong>${formatBytes(state.settings?.chunkSizeBytes ?? 10485760)} bounded reads</strong></div>
          <div class="kv"><span>Local cache</span><strong>No complete original copied by the app</strong></div>
          ${selectedFile ? `<div class="kv"><span>Path</span><strong class="path-detail">${escapeHTML(selectedFile.path)}</strong></div>` : ""}
        </div>
      </section>
    `;
  }

  if (state.activeView === "camera") {
    const selectedCount = state.media.filter((item) => item.selected).length;
    return `
      <section class="panel">
        <div class="panel-header">
          <div>
            <p class="eyebrow">Selected camera media</p>
            <h3>${escapeHTML(state.media.find((item) => item.selected)?.name ?? "No media selected")}</h3>
          </div>
          ${renderBadge(selectedCount > 0 ? `${selectedCount} selected` : "Empty", selectedCount > 0 ? "success" : "neutral")}
        </div>
        <div class="detail-body">
          <div class="kv"><span>Endpoint</span><strong>http://192.168.2.1/v2</strong></div>
          <div class="kv"><span>Range</span><strong>206 Partial Content required</strong></div>
          <div class="kv"><span>Destination</span><strong>${escapeHTML(destinationPath)}</strong></div>
          <div class="kv"><span>Analysis</span><strong>Queued after upload</strong></div>
        </div>
      </section>
    `;
  }

  if (state.activeView === "library") {
    const assets = state.libraryAssets;
    const totalSize = assets.reduce((sum, a) => sum + (a.sizeBytes ?? 0), 0);
    const analyzed = assets.filter(a => (a.analysisScenes ?? 0) > 0 || a.analysisJobId).length;
    return `
      <section class="panel">
        <div class="panel-header">
          <div>
            <p class="eyebrow">Library overview</p>
            <h3>${assets.length} asset${assets.length === 1 ? "" : "s"}</h3>
          </div>
          ${renderBadge(assets.length > 0 ? "Active" : "Empty", assets.length > 0 ? "success" : "neutral")}
        </div>
        <div class="detail-body">
          <div class="kv"><span>Total size</span><strong>${formatBytes(totalSize)}</strong></div>
          <div class="kv"><span>Analyzed</span><strong>${analyzed} / ${assets.length}</strong></div>
          <div class="kv"><span>Storage</span><strong>OneDrive / ${escapeHTML(destinationPath)}</strong></div>
          <div class="kv"><span>Sync</span><strong>Use "Scan OneDrive folder" to discover</strong></div>
        </div>
      </section>
    `;
  }

  if (state.activeView === "smart-edit") {
    const selectedJob = state.smartEdit.jobs.find((job) => job.id === state.smartEdit.selectedJobID) || state.smartEdit.jobs[0] || null;
    const selectedAssets = state.smartEdit.assets.filter((asset) => state.smartEdit.selectedAssetIDs.includes(asset.id));
    const selectedAssetNames = selectedAssets.map((asset) => escapeHTML(asset.name || asset.id)).join(", ");
    return `
      <section class="panel">
        <div class="panel-header">
          <div>
            <p class="eyebrow">Smart Edit summary</p>
            <h3>${selectedJob ? escapeHTML(selectedJob.assetName || selectedJob.assetId) : "No job selected"}</h3>
          </div>
          ${renderBadge(selectedJob ? selectedJob.status : "Empty", selectedJob ? (selectedJob.status === "succeeded" ? "success" : selectedJob.status === "failed" ? "danger" : "warning") : "neutral")}
        </div>
        <div class="detail-body">
          <div class="kv"><span>Selected assets</span><strong>${selectedAssets.length ? selectedAssetNames : "None"}</strong></div>
          <div class="kv"><span>Jobs</span><strong>${state.smartEdit.jobs.length}</strong></div>
          <div class="kv"><span>Configured</span><strong>${state.smartEdit.settings.status?.configured ? "Yes" : "No"}</strong></div>
          <div class="kv"><span>Endpoint</span><strong>${escapeHTML(state.smartEdit.settings.status?.endpoint || state.smartEdit.settings.endpoint || "—")}</strong></div>
          <p class="queue-message">Create edit projects only when a timeline draft is ready. Rendering stays in the Editing Studio.</p>
        </div>
      </section>
    `;
  }

  return "";
}

function renderActiveView(selectedCount: number): string {
  switch (state.activeView) {
    case "camera":
      return `${renderDJIProtocolPanel()}${renderCameraMediaPanel(selectedCount)}${renderTransferQueuePanel("Transfer queue", "Direct camera to OneDrive streaming")}`;
    case "transfers":
      return `${renderLocalMediaTable()}${renderTransferQueuePanel()}`;
    case "settings":
      return renderSettingsPanel();
    case "smart-edit":
      return renderVideoIndexerStudioPanel(state.smartEdit);
    case "editing":
          return renderEditingPanelWrapper();
    case "library":
          return renderLibraryPanel();
    case "analysis":
      return renderAnalysisPanel(state.analysis);
  }
}

function renderLibraryPanel(): string {
  const assets = state.libraryAssets;
  const totalSize = assets.reduce((sum, a) => sum + (a.sizeBytes ?? 0), 0);
  const analyzedCount = assets.filter(a => (a.analysisScenes ?? 0) > 0 || a.analysisJobId).length;
  const statusLine = assets.length === 0
    ? `No assets in library yet — upload local videos in the Transfers tab to start building your library.`
    : `OneDrive · ${assets.length} asset${assets.length === 1 ? "" : "s"} · ${formatBytes(totalSize)}${analyzedCount > 0 ? ` · ${analyzedCount} / ${assets.length} analyzed` : ""}`;

  const tableRows = assets.length === 0
    ? `<tr><td colspan="5" class="empty-state">${escapeHTML(statusLine)}</td></tr>`
    : assets.map((a) => {
        const analyzed = a.analysisJobId ? "yes" : "no";
        return `<tr>
          <td class="text-ellipsis" title="${escapeHTML(a.name || a.id)}">${escapeHTML(a.name || a.id)}</td>
          <td class="num">${formatBytes(a.sizeBytes ?? 0)}</td>
          <td><span class="status-badge ${analyzed === "yes" ? "tone-success" : "tone-neutral"}">${analyzed}</span></td>
          <td class="num">${a.analysisScenes ?? 0}</td>
          <td><span class="mono secondary">${a.id}</span></td>
        </tr>`;
      }).join("");

  const selectedId = selectedLibraryAssetID();
  const selected = selectedId ? assets.find(a => a.id === selectedId) : null;
  const detailPanel = selected ? renderLibraryAssetDetail(selected) : "";

  return `
    <section class="panel">
      <div class="panel-header">
        <h3>Project library</h3>
        <p class="secondary">${escapeHTML(statusLine)}</p>
              <div class="actions">
                <button class="button secondary" type="button" data-action="sync-onedrive">Scan OneDrive folder</button>
              </div>
            </div>
      <div class="layout-with-detail">
        <div class="layout-table">
          <table class="data-table" aria-label="Library assets">
            <thead><tr>
              <th>Name</th>
              <th class="num">Size</th>
              <th class="num">Analyzed</th>
              <th class="num">Scenes</th>
              <th>Asset ID</th>
            </tr></thead>
            <tbody>${tableRows}</tbody>
          </table>
        </div>
        ${detailPanel}
      </div>
    </section>`;
}

function selectedLibraryAssetID(): string | null {
  return state.selectedAssetId ?? null;
}

function renderLibraryAssetDetail(asset: ProjectAsset): string {
  const lines: [string, string][] = [
    ["Name", asset.name || ""],
    ["Cloud asset ID", asset.cloudAssetId || "—"],
    ["Size", formatBytes(asset.sizeBytes ?? 0)],
    ["Content type", asset.contentType || "—"],
    ["Analysis status", asset.analysisJobId ? `Job ${asset.analysisJobId}` : "Not submitted"],
    ["Scenes detected", `${asset.analysisScenes ?? 0}`],
    ["Created", formatDate(asset.createdAt)],
  ];
  return `
    <aside class="detail-body" aria-label="Asset detail">
      <button type="button" class="tab-close" data-action="clear-selection" title="Close">&times;</button>
      <h4>${escapeHTML(asset.name || asset.id)}</h4>
      <dl class="kv">
        ${lines.map(([k, v]) => `<div><dt>${escapeHTML(k)}</dt><dd>${escapeHTML(v)}</dd></div>`).join("")}
      </dl>
      <div class="detail-actions">
        <button type="button" class="button secondary" disabled>Submit to Azure CU</button>
      </div>
    </aside>`;
}

    function viewTitle(): string {
      switch (state.activeView) {
    case "camera":
      return "Camera import workspace";
    case "transfers":
      return "USB/local transfers";
    case "library":
      return "Project library";
    case "analysis":
      return "Analysis studio";
    case "smart-edit":
      return "Smart Edit Studio";
    case "editing":
      return "Editing studio";
    case "settings":
      return "Settings";
  }
}

function render(): void {
  const selectedCount = state.media.filter((item) => item.selected).length;
  const queue = visibleTransferQueue();
  const diagnosticRows: DiagnosticItem[] = state.diagnostics.length
    ? state.diagnostics
    : [{ label: "Services", detail: "Waiting for Wails runtime status.", tone: "neutral" }];
  const destination = state.settings?.oneDriveDestination;
  const destinationPath = destination?.path || "/Apps/AI Video Studio/Imports";

  root.innerHTML = `
    <main class="shell">
      <aside class="sidebar" aria-label="Primary workflow">
        <div class="brand">
          <span class="brand-mark" aria-hidden="true">AV</span>
          <div>
            <p class="eyebrow">Wails v3 desktop</p>
            <h1>${escapeHTML(state.title)}</h1>
          </div>
        </div>
        <nav>
          <button type="button" data-action="navigate" data-view="camera" aria-current="${state.activeView === "camera" ? "page" : "false"}">Camera <small>${state.media.length} files</small></button>
          <button type="button" data-action="navigate" data-view="transfers" aria-current="${state.activeView === "transfers" ? "page" : "false"}">Transfers <small>${queue.length} jobs</small></button>
          <button type="button" data-action="navigate" data-view="library" aria-current="${state.activeView === "library" ? "page" : "false"}">Library <small>${state.libraryAssets.length} assets</small></button>
          <button type="button" data-action="navigate" data-view="analysis" aria-current="${state.activeView === "analysis" ? "page" : "false"}">Analysis <small>${state.analysis.jobs.length} jobs</small></button>
          <button type="button" data-action="navigate" data-view="smart-edit" aria-current="${state.activeView === "smart-edit" ? "page" : "false"}">Smart Edit Studio <small>${state.smartEdit.jobs.length} jobs</small></button>
          <button type="button" data-action="navigate" data-view="editing" aria-current="${state.activeView === "editing" ? "page" : "false"}">Editing <small>1 render</small></button>
          <button type="button" data-action="navigate" data-view="settings" aria-current="${state.activeView === "settings" ? "page" : "false"}">Settings <small>safe</small></button>
        </nav>
        <section class="storage-note" aria-label="Storage policy">
          <strong>Storage policy</strong>
          <p>Original videos stream in bounded chunks to OneDrive. USB/local imports read from the selected source path without copying the complete file into the app cache.</p>
        </section>
      </aside>

      <section class="workspace" id="${state.activeView}">
        <header class="topbar">
          <div>
            <p class="eyebrow">DJI Osmo Action 4 to OneDrive 365 to Azure AI</p>
            <h2>${escapeHTML(viewTitle())}</h2>
            <p class="lede">${escapeHTML(state.description)}</p>
          </div>
          <div class="actions">
            <button class="button secondary" type="button" data-action="refresh">Run diagnostics</button>
            ${
              state.activeView === "transfers"
                ? `<button class="button" type="button" data-action="choose-local-files">Choose USB videos</button>`
                : state.activeView === "camera"
                  ? `<button class="button" type="button" data-action="start-transfer">Start selected transfer</button>`
                  : ""
            }
          </div>
        </header>

        <section class="status-strip" aria-label="System status">
          ${state.status
            .map(
              (item) => `
                <div class="strip-item" data-tone="${item.tone}">
                  <span>${escapeHTML(item.label)}</span>
                  <strong>${escapeHTML(item.value)}</strong>
                </div>
              `,
            )
            .join("")}
        </section>

        <section class="content">
          <div class="main-stack">
            ${renderActiveView(selectedCount)}
          </div>

          <aside class="details" aria-label="Details and diagnostics">
            ${renderDetailsPanel(destinationPath)}

            <section class="panel">
              <div class="panel-header">
                <div>
                  <p class="eyebrow">Setup checklist</p>
                  <h3>Operational readiness</h3>
                </div>
              </div>
              <div class="detail-body">
                ${diagnosticRows
                  .map(
                    (item) => `
                      <div class="diagnostic">
                        ${renderBadge(item.label, item.tone)}
                        <strong>${escapeHTML(item.detail)}</strong>
                      </div>
                    `,
                  )
                  .join("")}
              </div>
            </section>

            <section class="panel">
              <div class="panel-header">
                <div>
                  <p class="eyebrow">Render monitor</p>
                  <h3>FFmpeg export</h3>
                </div>
                ${renderBadge("Preview", "info")}
              </div>
              <div class="detail-body">
                ${renderProgress(67, "00:01:09 / 00:01:43 - libx264")}
                <pre class="log">frame=1842 fps=58 q=24.0
out_time=00:01:09.020
progress=continue</pre>
              </div>
            </section>
          </aside>
        </section>
      </section>
    </main>
  `;

  root.querySelectorAll<HTMLButtonElement>("[data-action='navigate']").forEach((button) => {
    button.addEventListener("click", () => {
      state.activeView = (button.dataset.view || "transfers") as AppView;
      render();
    });
  });

  root.querySelectorAll<HTMLButtonElement>("[data-action='refresh']").forEach((button) => {
    button.addEventListener("click", () => {
      void refreshServiceStatus();
    });
  });

  if (!mainDelegatedListenersInitialized) {
    mainDelegatedListenersInitialized = true;

    // Library table row clicks
    root.addEventListener("click", (event) => {
      const target = event.target as HTMLElement;
      const row = target.closest<HTMLTableRowElement>(".data-table tbody tr");
      if (!row || row.querySelector(".empty-state")) return;
      const cells = row.querySelectorAll("td");
      const assetId = cells.length === 5 ? cells[4]?.textContent?.trim() : null;
      if (assetId) {
        state.selectedAssetId = assetId;
        render();
      }
    });

    // Close detail panel
    root.addEventListener("click", (event) => {
      const target = event.target as HTMLElement;
      if (target.closest("[data-action='clear-selection']")) {
        state.selectedAssetId = null;
        render();
      }
    });

    // Sync OneDrive folder
    root.addEventListener("click", (event) => {
      const target = event.target as HTMLElement;
      if (target.closest("[data-action='sync-onedrive']")) {
        void syncOneDriveFolder();
      }
    });

    // Analysis Studio actions
    root.addEventListener("click", (event) => {
      const target = event.target as HTMLElement;

      if (target.closest("[data-action='analysis-refresh']")) {
        void refreshAnalysisState(state.analysis).then(() => render());
        return;
      }

      if (target.closest("[data-action='analysis-submit-pending']")) {
        void submitPendingAssetsAndRefresh();
        return;
      }

      const viewButton = target.closest<HTMLButtonElement>("[data-action='analysis-view-job']");
      if (viewButton) {
        state.analysis.selectedJobID = viewButton.dataset.jobId || null;
        state.analysis.view = "detail";
        render();
        return;
      }

      if (target.closest("[data-action='analysis-back']")) {
        state.analysis.view = "list";
        state.analysis.selectedJobID = null;
        render();
        return;
      }

      const resubmitButton = target.closest<HTMLButtonElement>("[data-action='analysis-resubmit']");
      if (resubmitButton) {
        const assetId = resubmitButton.dataset.assetId || "";
        if (assetId) {
          void resubmitAssetAndRefresh(assetId);
        }
      }
    });

    // Smart Edit Studio actions
    root.addEventListener("click", (event) => {
      const target = event.target as HTMLElement;

      if (target.closest("[data-action='video-indexer-save-settings']")) {
        void saveVideoIndexerSettings(
          state.smartEdit,
          state.smartEdit.settings.endpoint,
          state.smartEdit.settings.apiKey,
        ).then(() => render());
        return;
      }

      if (target.closest("[data-action='video-indexer-refresh']")) {
        void refreshVideoIndexerStudioState(state.smartEdit).then(() => {
          state.libraryAssets = state.smartEdit.assets;
          render();
        });
        return;
      }

      if (target.closest("[data-action='video-indexer-submit-selected']")) {
        void submitVideoIndexerAssets(state.smartEdit).then(({ submitted, failed }) => {
          state.smartEdit.message =
            submitted === 0 && failed === 0
              ? "No eligible selected assets were found."
              : `Submitted ${submitted} asset${submitted === 1 ? "" : "s"}${failed ? `, ${failed} failed` : ""}.`;
          state.libraryAssets = state.smartEdit.assets;
          render();
        });
        return;
      }

      if (target.closest("[data-action='video-indexer-submit-pending']")) {
        void submitVideoIndexerAssets(state.smartEdit, []).then(({ submitted, failed }) => {
          state.smartEdit.message =
            submitted === 0 && failed === 0
              ? "No pending assets were available."
              : `Submitted ${submitted} pending asset${submitted === 1 ? "" : "s"}${failed ? `, ${failed} failed` : ""}.`;
          state.libraryAssets = state.smartEdit.assets;
          render();
        });
        return;
      }

      const toggleAsset = target.closest<HTMLButtonElement | HTMLInputElement>("[data-action='video-indexer-toggle-asset']");
      if (toggleAsset) {
        toggleVideoIndexerAssetSelection(state.smartEdit, toggleAsset.dataset.assetId || "");
        render();
        return;
      }

      const viewJob = target.closest<HTMLButtonElement>("[data-action='video-indexer-view-job']");
      if (viewJob) {
        selectVideoIndexerJob(state.smartEdit, viewJob.dataset.jobId || null);
        render();
        return;
      }

      const cancelJob = target.closest<HTMLButtonElement>("[data-action='video-indexer-cancel-job']");
      if (cancelJob) {
        const jobID = cancelJob.dataset.jobId || "";
        if (jobID) {
          void cancelVideoIndexerJob(state.smartEdit, jobID)
            .then(() => {
              state.smartEdit.selectedJobID = jobID;
              render();
            })
            .catch((error) => {
              state.smartEdit.message = error instanceof Error ? error.message : "Canceling the Video Indexer job failed.";
              render();
            });
        }
        return;
      }

      const createProject = target.closest<HTMLButtonElement>("[data-action='video-indexer-create-project']");
      if (createProject) {
        const jobID = createProject.dataset.jobId || "";
        const suggestionID = createProject.dataset.suggestionId || "";
        if (jobID) {
          void createEditProjectFromVideoIndexerJob(state.smartEdit, jobID, suggestionID)
            .then((project) => {
              if (project) {
                state.activeView = "editing";
                state.editing.activeProject = project;
                void loadEditingData(state.editing).then(() => render());
                return;
              }
              render();
            })
            .catch((error) => {
              state.smartEdit.message = error instanceof Error ? error.message : "Creating the edit project failed.";
              render();
            });
        }
        return;
      }
    });

        // Editing Studio actions
        root.addEventListener("click", (event) => {
          const target = event.target as HTMLElement;

          // Add asset to timeline
          const addBtn = target.closest<HTMLElement>("[data-action='add-asset']");
          if (addBtn) {
            const assetId = addBtn.dataset.assetId || "";
            const asset = state.editing.assets.find((a) => a.id === assetId);
            if (asset) {
              void addClipToTimeline(state.editing, asset).then(() => render());
            }
            return;
          }

          // Remove clip
          const removeBtn = target.closest<HTMLElement>("[data-action='remove-clip']");
          if (removeBtn) {
            removeClip(state.editing, removeBtn.dataset.clipId || "");
            render();
            return;
          }

          // Move clip up
          const upBtn = target.closest<HTMLElement>("[data-action='move-up']");
          if (upBtn) {
            moveClipUp(state.editing, upBtn.dataset.clipId || "");
            render();
            return;
          }

          // Move clip down
          const downBtn = target.closest<HTMLElement>("[data-action='move-down']");
          if (downBtn) {
            moveClipDown(state.editing, downBtn.dataset.clipId || "");
            render();
            return;
          }

          // Save project
          const saveBtn = target.closest<HTMLElement>("[data-action='save-project']");
          if (saveBtn) {
            void saveProject(state.editing).then(() => render());
            return;
          }

          // Start render
          const renderBtn = target.closest<HTMLElement>("[data-action='start-render']");
          if (renderBtn) {
            void startRender(state.editing).then(() => render());
            return;
          }

          // Select project
          const selectProject = target.closest<HTMLElement>("[data-action='select-project']");
          if (selectProject) {
            const pid = selectProject.dataset.projectId || "";
            const found = state.editing.projects.find((p) => p.id === pid);
            if (found) {
              state.editing.activeProject = found;
              render();
            }
            return;
          }

          // New project
          const newProjectBtn = target.closest<HTMLElement>("[data-action='new-project']");
          if (newProjectBtn) {
            void createNewProject(state.editing, `Edit ${new Date().toLocaleDateString()}`).then(() => render());
            return;
          }
        });

        // Preset change via select
        root.addEventListener("change", (event) => {
          const target = event.target as HTMLSelectElement;
          if (target.dataset.action === "set-preset" && state.editing.activeProject) {
            state.editing.activeProject.renderPreset = target.value;
          }
        });

        root.addEventListener("input", (event) => {
          const target = event.target as HTMLInputElement;
          if (target.dataset.setting === "video-indexer-endpoint") {
            state.smartEdit.settings.endpoint = target.value;
            return;
          }
          if (target.dataset.setting === "video-indexer-apikey") {
            state.smartEdit.settings.apiKey = target.value;
          }
        });
  }

  root.querySelector<HTMLButtonElement>("[data-action='start-transfer']")?.addEventListener("click", () => {
        void startSelectedTransfer();
  });

  root.querySelectorAll<HTMLButtonElement>("[data-action='choose-local-files']").forEach((button) => {
    button.addEventListener("click", () => {
      void chooseLocalFiles();
    });
  });

  root.querySelector<HTMLButtonElement>("[data-action='start-local-transfer']")?.addEventListener("click", () => {
    void startLocalTransfer();
  });

  root.querySelector<HTMLInputElement>("[data-action='toggle-local-all']")?.addEventListener("change", (event) => {
    const checked = (event.currentTarget as HTMLInputElement).checked;
    state.localMedia = state.localMedia.map((file) => ({ ...file, selected: checked }));
    render();
  });

  root.querySelectorAll<HTMLInputElement>("[data-action='toggle-local-file']").forEach((input) => {
    input.addEventListener("change", () => {
      const id = input.dataset.localId || "";
      state.localMedia = state.localMedia.map((file) => (file.id === id ? { ...file, selected: input.checked } : file));
      render();
    });
  });

  root.querySelector<HTMLButtonElement>("[data-action='scan-ble']")?.addEventListener("click", () => {
    void scanBLEDevices();
  });

  root.querySelectorAll<HTMLButtonElement>("[data-action='select-ble-device']").forEach((button) => {
    button.addEventListener("click", () => {
      state.selectedBleDeviceId = button.dataset.deviceId || "";
      state.bleActionMessage = state.selectedBleDeviceId ? `Selected BLE device ${state.selectedBleDeviceId}.` : state.bleActionMessage;
      render();
    });
  });

  root.querySelector<HTMLButtonElement>("[data-action='pair-ble']")?.addEventListener("click", () => {
    void pairSelectedBLEDevice();
  });

  root.querySelector<HTMLButtonElement>("[data-action='save-settings']")?.addEventListener("click", () => {
    void saveSettingsFromForm();
  });

  root.querySelector<HTMLButtonElement>("[data-action='start-onedrive-auth']")?.addEventListener("click", () => {
    void startOneDriveAuth();
  });

  root.querySelector<HTMLButtonElement>("[data-action='poll-onedrive-auth']")?.addEventListener("click", () => {
    void pollOneDriveAuth();
  });

  root.querySelector<HTMLButtonElement>("[data-action='sign-out-onedrive']")?.addEventListener("click", () => {
    void signOutOneDrive();
  });
}

async function readServiceStatus(): Promise<void> {
  const [overview, camera, graph, azure, ffmpeg, settings, djiStatus, djiProtocol, djiDiagnostics] = await Promise.allSettled([
    AppService.GetOverview(),
    CameraService.GetConnectionStatus(),
    OneDriveService.Status(),
    ContentUnderstandingService.Status(),
    VideoProcessingService.RuntimeStatus(),
    SettingsService.Get(),
    DJIControlService.Status(),
    DJIControlService.ProtocolProfile(),
    DJIControlService.RunDiagnostics("osmo-action-4"),
  ]);

  if (overview.status === "fulfilled") {
    state.title = overview.value.name || state.title;
    state.version = overview.value.version || state.version;
    state.description = overview.value.description || state.description;
  }

  if (settings.status === "fulfilled") {
    state.settings = settings.value;
  }

  if (djiStatus.status === "fulfilled") {
    state.djiStatus = djiStatus.value;
  }
  if (djiProtocol.status === "fulfilled") {
    state.djiProtocol = djiProtocol.value;
  }
  if (djiDiagnostics.status === "fulfilled") {
    state.djiDiagnostics = djiDiagnostics.value;
  }

  const cameraStatus =
    camera.status === "fulfilled"
      ? {
          value: camera.value.available ? "Camera connected" : "Connector stubbed",
          detail: camera.value.message,
          tone: camera.value.available ? ("success" as const) : ("warning" as const),
        }
      : { value: "Unavailable", detail: "Camera service could not be reached.", tone: "danger" as const };

  const graphStatus =
    graph.status === "fulfilled"
      ? {
          value: graph.value.authStatus === "signed_in" ? "Signed in" : graph.value.scope || "Not configured",
          detail: graph.value.message || "Microsoft Graph status is available.",
          tone: graph.value.authStatus === "signed_in" ? ("success" as const) : ("warning" as const),
        }
      : { value: "Unavailable", detail: "OneDrive service could not be reached.", tone: "danger" as const };

  const azureStatus =
    azure.status === "fulfilled"
      ? {
          value: azure.value.configured ? azure.value.analyzerId || "Configured" : "Not configured",
          detail: azure.value.message,
          tone: azure.value.configured ? ("success" as const) : ("warning" as const),
        }
      : { value: "Unavailable", detail: "Azure Content Understanding service could not be reached.", tone: "danger" as const };

  if (azure.status === "fulfilled") {
    state.analysis.cuConfigured = azure.value.configured;
  }
  state.analysis.mediaServiceConfigured = Boolean(
    settings.status === "fulfilled" && settings.value.mediaServiceEndpoint,
  );

  const ffmpegStatus =
    ffmpeg.status === "fulfilled"
      ? {
          value: ffmpeg.value.available ? "Runtime detected" : "Runtime missing",
          detail: ffmpeg.value.message,
          tone: ffmpeg.value.available ? ("success" as const) : ("warning" as const),
        }
      : { value: "Unavailable", detail: "Video processing service could not be reached.", tone: "danger" as const };

  state.status = [
    { label: "Camera", value: cameraStatus.value, tone: cameraStatus.tone },
    { label: "OneDrive", value: graphStatus.value, tone: graphStatus.tone },
    { label: "Azure CU", value: azureStatus.value, tone: azureStatus.tone },
    { label: "FFmpeg", value: ffmpegStatus.value, tone: ffmpegStatus.tone },
    {
      label: "Active work",
      value: `${visibleTransferQueue().length} transfers, 0 renders, ${state.smartEdit.jobs.filter((job) => job.status === "submitted" || job.status === "polling").length} VI jobs`,
      tone:
        visibleTransferQueue().length || state.smartEdit.jobs.filter((job) => job.status === "submitted" || job.status === "polling").length
          ? "info"
          : "neutral",
    },
  ];

  state.diagnostics = [
    { label: "Camera", detail: cameraStatus.detail, tone: cameraStatus.tone },
    {
      label: "BLE/DUML",
      detail:
        state.djiStatus?.message ||
        state.djiDiagnostics?.message ||
        "DJI control adapter status could not be loaded.",
      tone: state.djiStatus?.adapterConfigured ? "success" : "warning",
    },
    { label: "Microsoft 365", detail: graphStatus.detail, tone: graphStatus.tone },
    { label: "Azure CU", detail: azureStatus.detail, tone: azureStatus.tone },
    { label: "FFmpeg", detail: ffmpegStatus.detail, tone: ffmpegStatus.tone },
  ];
  state.loading = false;
}

async function refreshServiceStatus(): Promise<void> {
  state.loading = true;
  state.transferMessage = "Refreshing service diagnostics...";
  render();
  await readServiceStatus();
  await refreshTransferJobs();
  await refreshVideoIndexerStudioState(state.smartEdit);
  state.libraryAssets = state.smartEdit.assets;
  state.analysis.assets = state.smartEdit.assets;
  state.transferMessage = "Diagnostics refreshed from Wails services.";
  render();
}

async function startSelectedTransfer(): Promise<void> {
  const selected = state.media.filter((item) => item.selected);
  if (selected.length === 0) {
    state.transferMessage = "Select at least one camera media item before starting a transfer.";
    render();
    return;
  }

  state.transferMessage = "Creating transfer job...";
  render();

  try {
    const job = await TransferService.StartCameraToOneDrive(
      new StartTransferRequest({
        cameraDeviceId: "osmo-action-4",
        mediaIds: selected.map((item) => item.id),
        destinationPath: "/Apps/AI Video Studio/Imports",
      }),
    );
    state.transferMessage = job.message || `Transfer job ${job.id} is ${job.status}.`;
  } catch (error) {
    state.transferMessage = error instanceof Error ? error.message : "Transfer service call failed.";
  }

  render();
}

async function submitPendingAssetsAndRefresh(): Promise<void> {
  const pendingCount = state.analysis.assets.filter((a) => (a.analysisStatus ?? "none") === "none").length;
  if (pendingCount === 0) {
    state.analysis.message = "No pending assets to submit.";
    render();
    return;
  }
  const confirmed = window.confirm(
    `Submit ${pendingCount} asset${pendingCount === 1 ? "" : "s"} for AI analysis? This will use Azure Content Understanding.`,
  );
  if (!confirmed) return;
  const { submitted, failed } = await submitPendingAssets(state.analysis);
  state.analysis.message =
    submitted === 0 && failed === 0
      ? "No pending assets to submit."
      : `Submitted ${submitted} asset${submitted === 1 ? "" : "s"} for analysis${failed ? `, ${failed} failed to submit` : ""}.`;
  await refreshAnalysisState(state.analysis);
  await loadLibraryAssets();
  render();
}

async function resubmitAssetAndRefresh(assetId: string): Promise<void> {
  try {
    await submitAssetForAnalysis(assetId);
    state.analysis.message = "Re-submitted asset for analysis.";
  } catch (error) {
    state.analysis.message = error instanceof Error ? error.message : "Re-submitting asset failed.";
  }
  state.analysis.view = "list";
  state.analysis.selectedJobID = null;
  await refreshAnalysisState(state.analysis);
  render();
}

async function loadLibraryAssets(): Promise<void> {
  try {
    const lib = await ProjectLibraryService.Current();
    state.libraryAssets = lib.assets ?? [];
    state.analysis.assets = state.libraryAssets;
    state.smartEdit.assets = state.libraryAssets;
    state.smartEdit.selectedAssetIDs = state.smartEdit.selectedAssetIDs.filter((assetID) =>
      state.libraryAssets.some((asset) => asset.id === assetID),
    );
  } catch {
    state.libraryAssets = [];
    state.analysis.assets = [];
    state.smartEdit.assets = [];
    state.smartEdit.selectedAssetIDs = [];
    state.transferMessage = "Project library could not be loaded.";
  }
}

async function syncOneDriveFolder(): Promise<void> {
  const button = document.querySelector<HTMLButtonElement>("[data-action='sync-onedrive']");
  if (button) button.disabled = true;
  try {
    const folder = state.settings?.oneDriveDestination?.path || "/Apps/AI Video Studio/Imports";
    const added = await ProjectLibraryService.SyncWithOneDrive(folder);
    state.transferMessage = added > 0
      ? `Found ${added} new asset${added === 1 ? "" : "s"} in OneDrive folder "${folder}".`
      : `OneDrive folder "${folder}" is already in sync — no new assets found.`;
    await loadLibraryAssets();
  } catch (err) {
    state.transferMessage = err instanceof Error ? err.message : "OneDrive sync failed.";
  } finally {
    if (button) button.disabled = false;
    render();
  }
}

function jobTone(status: string): Tone {
  switch (status) {
    case "completed":
      return "success";
    case "running":
    case "queued":
      return "info";
    case "failed":
    case "blocked":
      return "danger";
    case "cancelled":
      return "warning";
    default:
      return "neutral";
  }
}

function queueItemFromJob(job: TransferJob): QueueItem {
  const progress = job.bytesTotal > 0 ? Math.round((job.bytesCompleted / job.bytesTotal) * 100) : 0;
  return {
    name: job.sourcePath || job.id,
    detail: job.destination || "OneDrive upload",
    progress,
    metric: `${formatBytes(job.bytesCompleted)} / ${formatBytes(job.bytesTotal)}`,
    status: job.status || "queued",
    tone: jobTone(job.status),
  };
}

async function refreshTransferJobs(renderAfter = false): Promise<void> {
  if (transferQueueRefreshInFlight) {
    return;
  }
  transferQueueRefreshInFlight = true;
  try {
    const jobs = await TransferService.ListJobs();
    state.queue = jobs.map(queueItemFromJob);
  } catch (error) {
    state.transferMessage = error instanceof Error ? error.message : "Transfer queue could not be refreshed.";
  } finally {
    transferQueueRefreshInFlight = false;
  }
  if (renderAfter) {
    render();
  }
}

function startTransferQueuePolling(): () => void {
  if (transferQueuePollID !== undefined) {
    window.clearInterval(transferQueuePollID);
  }
  void refreshTransferJobs(true);
  transferQueuePollID = window.setInterval(() => {
    void refreshTransferJobs(true);
  }, 1000);
  return () => {
    if (transferQueuePollID !== undefined) {
      window.clearInterval(transferQueuePollID);
      transferQueuePollID = undefined;
    }
  };
}

async function chooseLocalFiles(): Promise<void> {
  state.transferMessage = "Choose video files from the mounted Osmo USB drive...";
  render();

  try {
    const paths = await chooseLocalVideoPaths();
    if (!paths.length) {
      state.transferMessage = "No local video file selected.";
      render();
      return;
    }
    const files = await TransferService.DescribeLocalFiles(new DescribeLocalFilesRequest({ paths }));
    const existing = new Map(state.localMedia.map((file) => [file.id, file]));
    state.localMedia = files.map((file) => ({
      ...file,
      selected: existing.get(file.id)?.selected ?? true,
      readiness: "Ready",
      tone: "success",
    }));
    state.activeView = "transfers";
    state.transferMessage = `${files.length} local video file(s) ready for OneDrive upload.`;
  } catch (error) {
    state.transferMessage = error instanceof Error ? error.message : "Local video selection failed.";
  }

  render();
}

async function chooseLocalVideoPaths(): Promise<string[]> {
  try {
    return normalizeSelectedPaths(await FileDialogService.ChooseLocalVideos());
  } catch (serviceError) {
    const fallbackPaths = await Dialogs.OpenFile({
      Title: "Choose Osmo video files",
      Message: "Select MP4/MOV/M4V/LRV files from the USB-mounted camera or another local disk.",
      ButtonText: "Select videos",
      CanChooseFiles: true,
      CanChooseDirectories: false,
      AllowsMultipleSelection: true,
      AllowsOtherFiletypes: false,
      Filters: [{ DisplayName: "Video files", Pattern: "*.mp4;*.mov;*.m4v;*.lrv" }],
    });
    const paths = normalizeSelectedPaths(fallbackPaths);
    if (!paths.length && serviceError instanceof Error) {
      state.transferMessage = serviceError.message;
    }
    return paths;
  }
}

function normalizeSelectedPaths(value: string | string[] | null | undefined): string[] {
  if (Array.isArray(value)) {
    return value.filter((path) => path.trim().length > 0);
  }
  if (typeof value === "string" && value.trim().length > 0) {
    return [value];
  }
  return [];
}

async function startLocalTransfer(): Promise<void> {
  if (state.localTransferInFlight) {
    state.transferMessage = "A local OneDrive upload is already running. Wait for it to finish before starting another one.";
    render();
    return;
  }

  const selected = selectedLocalFiles();
  if (!selected.length) {
    state.transferMessage = "Select at least one local video before uploading.";
    render();
    return;
  }

  const destinationPath = "Imports";
  state.localTransferInFlight = true;
  state.transferMessage = `Uploading ${selected.length} local video file(s) to OneDrive...`;
  render();
  const stopPolling = startTransferQueuePolling();

  try {
    const job = await TransferService.StartLocalToOneDrive(
      new LocalToOneDriveRequest({
        files: selected.map((file) => new LocalMediaFile(file)),
        destinationPath,
        chunkSizeBytes: state.settings?.chunkSizeBytes,
      }),
    );
    state.transferMessage = job.message || `Local upload job ${job.id} is ${job.status}.`;
  } catch (error) {
    state.transferMessage = error instanceof Error ? error.message : "Local upload failed.";
  } finally {
    stopPolling();
    state.localTransferInFlight = false;
    await refreshTransferJobs();
  }

  render();
}

async function scanBLEDevices(): Promise<void> {
  state.bleActionMessage = "Scanning Windows BLE advertisements for Osmo/DJI candidates...";
  render();

  try {
    const devices = await DJIControlService.ScanBLE();
    state.bleDevices = devices;
    const firstCandidate =
      devices.find((device) => /osmo|dji|action/i.test(`${device.name} ${device.model} ${(device.serviceUuids || []).join(" ")}`)) ??
      devices[0];
    state.selectedBleDeviceId = firstCandidate?.id ?? "";
    state.bleActionMessage = devices.length
      ? `Scan complete: ${devices.length} BLE peripheral(s) found. Select the Osmo candidate, then run Pair / GATT test.`
      : "Scan complete: no BLE peripherals found. Check Windows Bluetooth and camera wireless/app control mode.";
    await readServiceStatus();
  } catch (error) {
    state.bleActionMessage = error instanceof Error ? error.message : "BLE scan failed.";
  }

  render();
}

async function pairSelectedBLEDevice(): Promise<void> {
  if (!state.selectedBleDeviceId) {
    state.bleActionMessage = "Select a scanned BLE device before pairing.";
    render();
    return;
  }

  const pin = state.djiProtocol?.ble?.defaultPin || "love";
  state.bleActionMessage = `Connecting to ${state.selectedBleDeviceId} and checking DJI GATT service fff0...`;
  render();

  try {
    const result = await DJIControlService.Pair(new PairingRequest({ deviceId: state.selectedBleDeviceId, pin }));
    state.bleActionMessage = result.message || (result.paired ? "Pairing/GATT test succeeded." : "Pairing/GATT test needs confirmation.");
    await readServiceStatus();
  } catch (error) {
    state.bleActionMessage = error instanceof Error ? error.message : "BLE pairing/GATT test failed.";
  }

  render();
}

function settingValue(name: string): string {
  return root.querySelector<HTMLInputElement>(`[data-setting='${name}']`)?.value.trim() ?? "";
}

async function saveSettingsFromForm(): Promise<void> {
  const current = state.settings ?? new AppSettings();
  const tenantId = settingValue("tenant") || "organizations";
  const clientId = settingValue("client");
  const folder = settingValue("folder") || "AI Video Studio";
  const chunkSize = Number(settingValue("chunk")) || 10 * 1024 * 1024;
  const destinationPath = `/Apps/${folder}/Imports`;
  const mediaServiceEndpoint = settingValue("media-endpoint");
  const mediaServiceApiKey = settingValue("media-apikey");

  state.settingsMessage = "Saving OneDrive settings...";
  render();

  try {
    state.settings = await SettingsService.Save(
      new AppSettings({
        ...current,
        tenantId,
        clientId,
        oneDriveFolder: folder,
        chunkSizeBytes: chunkSize,
        mediaServiceEndpoint,
        graphAuth: {
          ...current.graphAuth,
          tenantId,
          clientId,
          authFlow: AuthFlow.AuthFlowDeviceCode,
          scopes: current.graphAuth?.scopes?.length ? current.graphAuth.scopes : ["Files.ReadWrite.AppFolder"],
        },
        oneDriveDestination: {
          ...current.oneDriveDestination,
          mode: "app_folder",
          displayName: "AI Video Studio app folder",
          path: destinationPath,
        },
      }),
    );
    // The API key is json:"-" and never round-trips through Save, so it is
    // persisted separately. Leaving the field blank keeps the stored key.
    if (mediaServiceApiKey) {
      await SettingsService.SetMediaServiceEndpoint(mediaServiceEndpoint, mediaServiceApiKey);
      state.settings = await SettingsService.Get();
    }
    const apiKeyField = root.querySelector<HTMLInputElement>("[data-setting='media-apikey']");
    if (apiKeyField) {
      apiKeyField.value = "";
    }
    state.settingsMessage = "Settings saved. You can start OneDrive sign-in.";
    await readServiceStatus();
  } catch (error) {
    state.settingsMessage = error instanceof Error ? error.message : "Saving settings failed.";
  }

  render();
}

async function startOneDriveAuth(): Promise<void> {
  state.settingsMessage = "Requesting Microsoft device-code sign-in...";
  render();

  try {
    state.authChallenge = await OneDriveService.StartDeviceCodeAuth();
    state.settingsMessage = "Open the Microsoft verification page and enter the displayed code, then click Check sign-in.";
  } catch (error) {
    state.settingsMessage = error instanceof Error ? error.message : "OneDrive sign-in could not start.";
  }

  await readServiceStatus();
  render();
}

async function pollOneDriveAuth(): Promise<void> {
  state.settingsMessage = "Checking Microsoft sign-in status...";
  render();

  try {
    const auth = await OneDriveService.PollDeviceCodeAuth();
    state.settingsMessage = auth.message || "Microsoft sign-in status updated.";
    if (auth.status === "signed_in") {
      state.authChallenge = null;
    }
  } catch (error) {
    state.settingsMessage = error instanceof Error ? error.message : "OneDrive sign-in check failed.";
  }

  await readServiceStatus();
  render();
}

async function signOutOneDrive(): Promise<void> {
  try {
    await OneDriveService.SignOut();
    state.authChallenge = null;
    state.settingsMessage = "Signed out from the in-memory Microsoft Graph session.";
  } catch (error) {
    state.settingsMessage = error instanceof Error ? error.message : "Sign-out failed.";
  }

  await readServiceStatus();
  render();
}

render();
void refreshServiceStatus();
void refreshAnalysisState(state.analysis).then(() => render());
setupAnalysisEvents(state.analysis, () => render());
setupVideoIndexerStudioEvents(state.smartEdit, () => {
  state.libraryAssets = state.smartEdit.assets;
  state.analysis.assets = state.smartEdit.assets;
  render();
});
setupEditingEvents(state.editing, () => render());
void loadVideoIndexerStudioState(state.smartEdit).then(() => {
  state.libraryAssets = state.smartEdit.assets;
  render();
});
void loadEditingData(state.editing).then(() => render());
