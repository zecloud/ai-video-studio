// Analysis Studio — submits assets to Azure Content Understanding and shows
// scenes, transcripts, highlights, and edit suggestions for completed jobs.

import { Events } from "@wailsio/runtime";
import { ProjectLibraryService } from "../bindings/github.com/zecloud/ai-video-studio/internal/backend/index.js";
import { AnalysisJob, ProjectAsset } from "../bindings/github.com/zecloud/ai-video-studio/internal/library/models.js";
import {
  EditSuggestion,
  HighlightCandidate,
  Scene,
  TranscriptSegment,
} from "../bindings/github.com/zecloud/ai-video-studio/internal/contentunderstanding/models.js";

export interface AnalysisViewState {
  jobs: AnalysisJob[];
  assets: ProjectAsset[];
  selectedJobID: string | null;
  view: "list" | "detail";
  polling: boolean;
  message: string;
  cuConfigured: boolean;
  mediaServiceConfigured: boolean;
}

export function createAnalysisState(): AnalysisViewState {
  return {
    jobs: [],
    assets: [],
    selectedJobID: null,
    view: "list",
    polling: false,
    message: "",
    cuConfigured: true,
    mediaServiceConfigured: true,
  };
}

const IN_FLIGHT_STATUSES = new Set(["pending", "submitted", "polling"]);

function escapeHTML(value: string): string {
  return (value ?? "").replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;").replaceAll('"', "&quot;");
}

function badge(label: string, tone: "success" | "warning" | "danger" | "info" | "neutral"): string {
  return `<span class="badge ${tone}">${escapeHTML(label)}</span>`;
}

function statusTone(status: string): "success" | "warning" | "danger" | "info" | "neutral" {
  switch (status) {
    case "succeeded":
      return "success";
    case "failed":
      return "danger";
    case "polling":
      return "warning";
    case "submitted":
      return "info";
    default:
      return "neutral";
  }
}

function statusLabel(status: string): string {
  switch (status) {
    case "succeeded":
      return "Completed";
    case "failed":
      return "Failed";
    case "polling":
      return "Polling";
    case "submitted":
      return "Submitted";
    case "pending":
      return "Pending";
    default:
      return status || "Unknown";
  }
}

function formatMs(ms: number | undefined | null): string {
  const total = Math.max(0, Math.round((ms ?? 0) / 1000));
  const minutes = Math.floor(total / 60);
  const seconds = total % 60;
  return `${minutes.toString().padStart(2, "0")}:${seconds.toString().padStart(2, "0")}`;
}

function formatRelative(value: string | undefined): string {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  const diffMs = Date.now() - date.getTime();
  if (diffMs < 0) return "just now";
  const minutes = Math.floor(diffMs / 60000);
  if (minutes < 1) return "just now";
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  return `${days}d ago`;
}

/** Renders the full Analysis Studio panel (list or detail view). */
export function renderAnalysisPanel(state: AnalysisViewState): string {
  if (state.view === "detail" && state.selectedJobID) {
    const job = state.jobs.find((j) => j.id === state.selectedJobID);
    if (job) {
      return renderDetailView(job);
    }
  }
  return renderListView(state);
}

function renderConfigurationNotice(state: AnalysisViewState): string {
  if (!state.cuConfigured) {
    return `
      <section class="panel">
        <div class="detail-body empty-state">
          Azure Content Understanding is not configured. Go to Settings to set up your Azure endpoint and API key.
        </div>
      </section>`;
  }
  if (!state.mediaServiceConfigured) {
    return `
      <section class="panel">
        <div class="detail-body empty-state">
          Media Staging Service is not configured. Go to Settings to set up your Azure Container App endpoint.
        </div>
      </section>`;
  }
  return "";
}

function renderListView(state: AnalysisViewState): string {
  const configurationNotice = renderConfigurationNotice(state);
  const pendingAssets = state.assets.filter((a) => (a.analysisStatus ?? "none") === "none");
  const rows = state.jobs.length
    ? state.jobs
        .map((job) => {
          const tone = statusTone(job.status);
          const canView = job.status === "succeeded" || job.status === "failed";
          return `
            <tr>
              <td>${escapeHTML(job.assetName || job.assetId)}</td>
              <td>${badge(statusLabel(job.status), tone)}</td>
              <td class="muted">${formatRelative(job.createdAt)}</td>
              <td>
                ${
                  canView
                    ? `<button type="button" class="button secondary" data-action="analysis-view-job" data-job-id="${escapeHTML(job.id)}">View</button>`
                    : `<span class="muted">—</span>`
                }
              </td>
            </tr>`;
        })
        .join("")
    : `<tr><td colspan="4" class="empty-state">No analysis jobs yet. Submit assets from your library to get AI-powered scene detection, transcripts, highlights, and edit suggestions.</td></tr>`;

  const canSubmit = state.cuConfigured && state.mediaServiceConfigured && pendingAssets.length > 0;
  return `
    ${configurationNotice}
    <section class="panel">
      <div class="panel-header">
        <div>
          <p class="eyebrow">Azure Content Understanding</p>
          <h3>Analysis Studio</h3>
        </div>
        ${badge(`${state.jobs.length} job${state.jobs.length === 1 ? "" : "s"}`, state.jobs.length ? "info" : "neutral")}
      </div>
      <div class="detail-body">
        <div class="toolbar">
          <button type="button" class="button" data-action="analysis-submit-pending" ${canSubmit ? "" : "disabled"}>
            Submit ${pendingAssets.length ? `${pendingAssets.length} pending asset${pendingAssets.length === 1 ? "" : "s"}` : "selected assets"} to analysis
          </button>
          <button type="button" class="button secondary" data-action="analysis-refresh">Refresh</button>
        </div>
        ${state.message ? `<p class="queue-message">${escapeHTML(state.message)}</p>` : ""}
      </div>
      <div class="table-wrap">
        <table>
          <thead><tr><th>Asset</th><th>Status</th><th>Created</th><th>Actions</th></tr></thead>
          <tbody>${rows}</tbody>
        </table>
      </div>
      <div class="detail-body">
        <p class="muted">Showing ${state.jobs.length} job${state.jobs.length === 1 ? "" : "s"}</p>
      </div>
    </section>
  `;
}

function renderDetailView(job: AnalysisJob): string {
  const result = job.result;
  if (job.status === "failed") {
    return `
      <section class="panel">
        <div class="panel-header">
          <div>
            <p class="eyebrow">Analysis Studio</p>
            <h3>Analysis: ${escapeHTML(job.assetName || job.assetId)}</h3>
          </div>
          ${badge("Failed", "danger")}
        </div>
        <div class="detail-body">
          <button type="button" class="button secondary" data-action="analysis-back">&larr; Back to list</button>
          <p class="queue-message">Analysis failed: ${escapeHTML(job.errorMessage || "Unknown error")}. You can try re-submitting.</p>
          <button type="button" class="button" data-action="analysis-resubmit" data-asset-id="${escapeHTML(job.assetId)}">Re-submit for analysis</button>
        </div>
      </section>
    `;
  }

  if (!result) {
    return `
      <section class="panel">
        <div class="panel-header">
          <div>
            <p class="eyebrow">Analysis Studio</p>
            <h3>Analysis: ${escapeHTML(job.assetName || job.assetId)}</h3>
          </div>
          ${badge(statusLabel(job.status), statusTone(job.status))}
        </div>
        <div class="detail-body">
          <button type="button" class="button secondary" data-action="analysis-back">&larr; Back to list</button>
          <p class="queue-message"><span class="pulse-dot" aria-hidden="true"></span>Analysis in progress... This may take a few minutes depending on video length.</p>
        </div>
      </section>
    `;
  }

  return `
    <section class="panel">
      <div class="panel-header">
        <div>
          <p class="eyebrow">Analysis Studio</p>
          <h3>Analysis: ${escapeHTML(job.assetName || job.assetId)}</h3>
        </div>
        ${badge("Completed", "success")}
      </div>
      <div class="detail-body">
        <button type="button" class="button secondary" data-action="analysis-back">&larr; Back to list</button>
        <p class="queue-message">
          ${result.scenes.length} scenes, ${result.transcript.length} transcript segments,
          ${result.highlights.length} highlights, ${result.suggestions.length} edit suggestions.
        </p>
      </div>
    </section>
    ${renderScenesPanel(result.scenes)}
    ${renderTranscriptPanel(result.transcript)}
    ${renderHighlightsPanel(result.highlights)}
    ${renderSuggestionsPanel(result.suggestions)}
  `;
}

function renderScenesPanel(scenes: Scene[]): string {
  const rows = scenes.length
    ? scenes
        .map(
          (scene, index) => `
            <tr>
              <td class="num">${index + 1}</td>
              <td class="muted">${formatMs(scene.startMs)} – ${formatMs(scene.endMs)}</td>
              <td>${escapeHTML((scene.labels || []).join(", "))}</td>
              <td>${escapeHTML(scene.summary || "—")}</td>
              <td>${scene.highlight ? badge("Yes", "warning") : `<span class="muted">—</span>`}</td>
            </tr>`,
        )
        .join("")
    : `<tr><td colspan="5" class="empty-state">No scenes detected.</td></tr>`;

  return `
    <section class="panel">
      <div class="panel-header">
        <div>
          <p class="eyebrow">Scene detection</p>
          <h3>Scenes</h3>
        </div>
        ${badge(`${scenes.length}`, "info")}
      </div>
      <div class="table-wrap">
        <table>
          <thead><tr><th>#</th><th>Time</th><th>Labels</th><th>Summary</th><th>Highlight</th></tr></thead>
          <tbody>${rows}</tbody>
        </table>
      </div>
    </section>
  `;
}

function renderTranscriptPanel(segments: TranscriptSegment[]): string {
  const rows = segments.length
    ? segments
        .map(
          (seg) => `
            <div class="transcript-row">
              <span class="transcript-label">${formatMs(seg.startMs)}</span>
              <span class="transcript-text">${escapeHTML(seg.text)}</span>
            </div>`,
        )
        .join("")
    : `<div class="empty-state">No transcript available for this asset.</div>`;

  return `
    <section class="panel">
      <div class="panel-header">
        <div>
          <p class="eyebrow">Transcript</p>
          <h3>Transcript segments</h3>
        </div>
        ${badge(`${segments.length}`, "info")}
      </div>
      <div class="detail-body transcript-scroll">${rows}</div>
    </section>
  `;
}

function renderHighlightsPanel(highlights: HighlightCandidate[]): string {
  const rows = highlights.length
    ? highlights
        .map(
          (h) => `
            <div class="kv">
              <span>${escapeHTML(h.reason || "Highlight")}</span>
              <strong>${formatMs(h.startMs)} – ${formatMs(h.endMs)} · ${Math.round((h.score ?? 0) * 100)}%</strong>
            </div>`,
        )
        .join("")
    : `<div class="empty-state">No highlight candidates were found.</div>`;

  return `
    <section class="panel">
      <div class="panel-header">
        <div>
          <p class="eyebrow">Highlights</p>
          <h3>Highlight candidates</h3>
        </div>
        ${badge(`${highlights.length}`, "warning")}
      </div>
      <div class="detail-body">${rows}</div>
    </section>
  `;
}

function renderSuggestionsPanel(suggestions: EditSuggestion[]): string {
  const cards = suggestions.length
    ? suggestions
        .map(
          (s) => `
            <div class="suggestion-card">
              <h4>${escapeHTML(s.title)}</h4>
              <p>${escapeHTML(s.description)}</p>
            </div>`,
        )
        .join("")
    : `<div class="empty-state">No edit suggestions were generated.</div>`;

  return `
    <section class="panel">
      <div class="panel-header">
        <div>
          <p class="eyebrow">Edit suggestions</p>
          <h3>AI-generated proposals</h3>
        </div>
        ${badge(`${suggestions.length}`, "warning")}
      </div>
      <div class="detail-body suggestion-list">${cards}</div>
    </section>
  `;
}

function upsertJob(state: AnalysisViewState, job: AnalysisJob): void {
  const index = state.jobs.findIndex((j) => j.id === job.id);
  if (index >= 0) {
    state.jobs[index] = job;
  } else {
    state.jobs.unshift(job);
  }
  state.polling = state.jobs.some((j) => IN_FLIGHT_STATUSES.has(j.status));
}

/** Fetches the latest analysis jobs and library assets from the backend. */
export async function refreshAnalysisState(state: AnalysisViewState): Promise<void> {
  try {
    const [jobs, assets] = await Promise.all([ProjectLibraryService.AnalysisJobs(), ProjectLibraryService.Current()]);
    state.jobs = jobs ?? [];
    state.assets = assets?.assets ?? [];
    state.polling = state.jobs.some((j) => IN_FLIGHT_STATUSES.has(j.status));
  } catch (error) {
    state.message = error instanceof Error ? error.message : "Failed to refresh analysis jobs.";
  }
}

/** Submits an asset for Azure Content Understanding analysis. */
export async function submitAssetForAnalysis(assetID: string): Promise<void> {
  await ProjectLibraryService.SubmitForAnalysis(assetID);
}

/**
 * Submits every asset in the library that has not yet been analyzed
 * ('none' status). Returns counts of successes/failures for messaging.
 */
export async function submitPendingAssets(state: AnalysisViewState): Promise<{ submitted: number; failed: number }> {
  const pending = state.assets.filter((a) => (a.analysisStatus ?? "none") === "none");
  let submitted = 0;
  let failed = 0;
  for (const asset of pending) {
    try {
      await submitAssetForAnalysis(asset.id);
      submitted += 1;
    } catch {
      failed += 1;
    }
  }
  return { submitted, failed };
}

let pollTimer: number | undefined;

/**
 * Wires up 'analysis:progress' / 'analysis:completed' Wails events and a 5s
 * polling fallback while any job is in flight. Calls onChange after every
 * state mutation so the caller can re-render. Returns a teardown function.
 */
export function setupAnalysisEvents(state: AnalysisViewState, onChange: () => void = () => {}): () => void {
  const offProgress = Events.On("analysis:progress", (event) => {
    const job = AnalysisJob.createFrom(event.data as Partial<AnalysisJob>);
    upsertJob(state, job);
    onChange();
  });

  const offCompleted = Events.On("analysis:completed", (event) => {
    const job = AnalysisJob.createFrom(event.data as Partial<AnalysisJob>);
    upsertJob(state, job);
    onChange();
  });

  if (pollTimer !== undefined) {
    window.clearInterval(pollTimer);
  }
  pollTimer = window.setInterval(() => {
    if (!state.polling) return;
    void refreshAnalysisState(state).then(onChange);
  }, 5000);

  return () => {
    offProgress();
    offCompleted();
    if (pollTimer !== undefined) {
      window.clearInterval(pollTimer);
      pollTimer = undefined;
    }
  };
}
