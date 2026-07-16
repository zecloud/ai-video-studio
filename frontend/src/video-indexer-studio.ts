import { Events } from "@wailsio/runtime";
import {
  ProjectLibraryService,
  SettingsService,
  VideoIndexerStudioService,
} from "../bindings/github.com/zecloud/ai-video-studio/internal/backend/index.js";
import * as BackendModels from "../bindings/github.com/zecloud/ai-video-studio/internal/backend/models.js";
import * as EditingModels from "../bindings/github.com/zecloud/ai-video-studio/internal/editing/models.js";
import * as LibraryModels from "../bindings/github.com/zecloud/ai-video-studio/internal/library/models.js";
import * as VI from "../bindings/github.com/zecloud/ai-video-studio/internal/videoindexerstudio/models.js";

const IN_FLIGHT_STATUSES = new Set(["pending", "submitted", "polling"]);

export interface VideoIndexerStudioSettingsState {
  endpoint: string;
  apiKey: string;
  status: BackendModels.ProtectedEndpointStatus | null;
  message: string;
  saving: boolean;
}

export type VideoIndexerStudioAction =
  | { kind: "refresh" }
  | { kind: "generate-composition"; count: number }
  | { kind: "submit-selected"; count: number }
  | { kind: "submit-pending"; count: number }
  | { kind: "retry" | "cancel" | "create-project"; jobID: string }
  | { kind: "open-project"; projectID: string };

export interface VideoIndexerStudioViewState {
  jobs: BackendModels.VideoIndexerStudioJob[];
  assets: LibraryModels.ProjectAsset[];
  selectedAssetIDs: string[];
  selectedJobID: string | null;
  polling: boolean;
  message: string;
  activeAction: VideoIndexerStudioAction | null;
  settings: VideoIndexerStudioSettingsState;
}

interface CompositionSourceState {
  assetID: string;
  analysisJobID: string;
  status: string;
  durationMs: number;
}

export function createVideoIndexerStudioState(): VideoIndexerStudioViewState {
  return {
    jobs: [],
    assets: [],
    selectedAssetIDs: [],
    selectedJobID: null,
    polling: false,
    message: "",
    activeAction: null,
    settings: {
      endpoint: "",
      apiKey: "",
      status: null,
      message: "Configure the Video Indexer Studio endpoint and API key before submitting jobs.",
      saving: false,
    },
  };
}

function escapeHTML(value: string): string {
  return (value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;");
}

function badge(label: string, tone: "success" | "warning" | "danger" | "info" | "neutral"): string {
  return `<span class="badge ${tone}">${escapeHTML(label)}</span>`;
}

function formatMs(ms: number | undefined | null): string {
  const total = Math.max(0, Math.round((ms ?? 0) / 1000));
  const minutes = Math.floor(total / 60);
  const seconds = total % 60;
  return `${minutes.toString().padStart(2, "0")}:${seconds.toString().padStart(2, "0")}`;
}

function formatDurationNs(value: number | undefined | null): string {
  if (!Number.isFinite(value ?? NaN) || !value) return "—";
  const ms = Math.round((value ?? 0) / 1_000_000);
  if (ms < 1000) return `${ms}ms`;
  return formatMs(ms);
}

function formatDate(value: string | undefined): string {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  return date.toLocaleString();
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
  return `${Math.floor(hours / 24)}d ago`;
}

function firstNonEmpty(...values: Array<string | undefined | null>): string {
  for (const value of values) {
    if (value && value.trim()) {
      return value.trim();
    }
  }
  return "";
}

function stageLabel(status: string | undefined): string {
  switch ((status || "").toLowerCase()) {
    case "queued":
      return "Queued";
    case "staging":
      return "Staging";
    case "staged":
      return "Staged";
    case "processing":
      return "Processing";
    case "submitting":
      return "Submitting";
    case "indexing":
      return "Indexing";
    case "normalizing":
      return "Normalizing";
    case "generating":
      return "Generating";
    case "building_timeline":
      return "Building timeline";
    case "succeeded":
      return "Succeeded";
    case "failed":
      return "Failed";
    case "canceled":
      return "Canceled";
    case "running":
      return "Running";
    default:
      return status || "Unknown";
  }
}

function localStatusTone(status: string): "success" | "warning" | "danger" | "info" | "neutral" {
  switch (status) {
    case "complete":
    case "succeeded":
      return "success";
    case "failed":
      return "danger";
    case "polling":
      return "warning";
    case "submitted":
      return "info";
    case "canceled":
      return "neutral";
    default:
      return "neutral";
  }
}

function compositionSourceStates(job: BackendModels.VideoIndexerStudioJob, state: VideoIndexerStudioViewState): CompositionSourceState[] {
  if (job.compositionPlan?.sources?.length) {
    return job.compositionPlan.sources.map((source) => ({
      assetID: source.assetId,
      analysisJobID: source.analysisJobId,
      status: source.status,
      durationMs: source.durationMs,
    }));
  }

  return (job.assetIds || []).map((assetID, index) => {
    const analysisJobID = job.dependencyJobIds?.[index] || "";
    const analysis = state.jobs.find((candidate) => candidate.id === analysisJobID);
    return {
      assetID,
      analysisJobID,
      status: analysis?.status || "pending",
      durationMs: analysis?.videoIndexResult?.durationMs || 0,
    };
  });
}

function compositionSourceStatusLabel(status: string): string {
  return status === "complete" ? "Complete" : stageLabel(status);
}

function formatScore(score: number | undefined | null): string {
  if (!Number.isFinite(score ?? NaN)) return "Score unavailable";
  return `${Math.round(Math.max(0, Math.min(1, score ?? 0)) * 100)}%`;
}

function inFlight(status: string): boolean {
  return IN_FLIGHT_STATUSES.has(status);
}

function assetEligible(asset: LibraryModels.ProjectAsset): boolean {
  return firstNonEmpty(asset.cloudAssetId).length > 0;
}

function latestJobByAsset(jobs: BackendModels.VideoIndexerStudioJob[]): Map<string, BackendModels.VideoIndexerStudioJob> {
  const result = new Map<string, BackendModels.VideoIndexerStudioJob>();
  const sorted = [...jobs].sort((a, b) => (b.createdAt || "").localeCompare(a.createdAt || ""));
  for (const job of sorted) {
    if (job.composition) continue;
    if (!result.has(job.assetId)) {
      result.set(job.assetId, job);
    }
  }
  return result;
}

function upsertJob(state: VideoIndexerStudioViewState, next: BackendModels.VideoIndexerStudioJob): void {
  const index = state.jobs.findIndex((job) => job.id === next.id);
  if (index >= 0) {
    state.jobs[index] = next;
  } else {
    state.jobs.unshift(next);
  }
  state.jobs.sort((a, b) => (b.createdAt || "").localeCompare(a.createdAt || ""));
  state.polling = state.jobs.some((job) => inFlight(job.status));
}

function selectedAssets(state: VideoIndexerStudioViewState): LibraryModels.ProjectAsset[] {
  if (state.selectedAssetIDs.length === 0) return [];
  const selected = new Set(state.selectedAssetIDs);
  return state.assets.filter((asset) => selected.has(asset.id));
}

function pendingAssets(state: VideoIndexerStudioViewState): LibraryModels.ProjectAsset[] {
  const latest = latestJobByAsset(state.jobs);
  return state.assets.filter((asset) => {
    if (!assetEligible(asset)) return false;
    const job = latest.get(asset.id);
    return !job;
  });
}

export async function loadVideoIndexerStudioState(state: VideoIndexerStudioViewState): Promise<void> {
  try {
    const [assets, jobs, endpoint, status] = await Promise.all([
      ProjectLibraryService.ListAssets(),
      VideoIndexerStudioService.IndexingJobs(),
      SettingsService.GetVideoIndexerServiceEndpoint(),
      SettingsService.GetVideoIndexerServiceStatus(),
    ]);
    state.assets = assets ?? [];
    state.jobs = jobs ?? [];
    state.selectedAssetIDs = state.selectedAssetIDs.filter((assetID) => state.assets.some((asset) => asset.id === assetID));
    state.polling = state.jobs.some((job) => inFlight(job.status));
    state.settings.endpoint = endpoint ?? "";
    state.settings.apiKey = "";
    state.settings.status = status ?? null;
    state.settings.message = status?.message || state.settings.message;
  } catch (error) {
    state.message = error instanceof Error ? error.message : "Failed to load Video Indexer Studio data.";
  }
}

export async function refreshVideoIndexerStudioState(state: VideoIndexerStudioViewState): Promise<void> {
  if (state.activeAction) return;
  state.activeAction = { kind: "refresh" };
  const loadingMessage = "Refreshing Video Indexer jobs and assets...";
  state.message = loadingMessage;
  try {
    await loadVideoIndexerStudioState(state);
    if (state.message === loadingMessage) {
      state.message = "Video Indexer jobs and assets refreshed.";
    }
  } finally {
    state.activeAction = null;
  }
}

export function toggleVideoIndexerAssetSelection(state: VideoIndexerStudioViewState, assetID: string): void {
  const next = new Set(state.selectedAssetIDs);
  if (next.has(assetID)) {
    next.delete(assetID);
  } else {
    next.add(assetID);
  }
  state.selectedAssetIDs = Array.from(next);
}

export function selectVideoIndexerJob(state: VideoIndexerStudioViewState, jobID: string | null): void {
  state.selectedJobID = jobID;
}

export async function submitVideoIndexerAssets(state: VideoIndexerStudioViewState, assetIDs?: string[]): Promise<{ submitted: number; failed: number }> {
  if (state.activeAction) return { submitted: 0, failed: 0 };
  const ids = assetIDs === undefined ? state.selectedAssetIDs : assetIDs;
  const uniqueIDs = Array.from(new Set(ids));
  const targets = uniqueIDs.length
    ? state.assets.filter((asset) => uniqueIDs.includes(asset.id) && assetEligible(asset))
    : pendingAssets(state);
  const kind = assetIDs === undefined ? "submit-selected" : "submit-pending";
  state.activeAction = { kind, count: targets.length };
  state.message = `Submitting ${targets.length} asset${targets.length === 1 ? "" : "s"} to Video Indexer...`;
  let submitted = 0;
  let failed = 0;
  try {
    for (const asset of targets) {
      try {
        const job = await VideoIndexerStudioService.SubmitForIndexing(asset.id);
        if (job) {
          upsertJob(state, job);
        }

        submitted += 1;
      } catch {
        failed += 1;
      }
    }
    state.polling = state.jobs.some((job) => inFlight(job.status));
    return { submitted, failed };
  } finally {
    state.activeAction = null;
  }
}

export async function generateMultiVideoEdit(state: VideoIndexerStudioViewState): Promise<BackendModels.VideoIndexerStudioJob | null> {
  if (state.activeAction) return null;
  const targets = selectedAssets(state).filter(assetEligible);
  if (targets.length < 2) {
    throw new Error("Select at least two eligible videos to generate a combined edit.");
  }
  state.activeAction = { kind: "generate-composition", count: targets.length };
  state.message = `Preparing a combined edit from ${targets.length} videos...`;
  try {
    const job = await VideoIndexerStudioService.GenerateMultiVideoEdit(targets.map((asset) => asset.id));
    if (!job) {
      throw new Error("Smart Edit Studio did not return the combined edit job.");
    }
    upsertJob(state, job);
    state.selectedJobID = job.id;
    state.message = job.status === "succeeded"
      ? `Combined edit is ready for ${targets.length} videos.`
      : `Combined edit is waiting for ${targets.length} source analyses.`;
    return job;
  } finally {
    state.activeAction = null;
  }
}

export async function retryVideoIndexerJob(state: VideoIndexerStudioViewState, jobID: string): Promise<void> {
  if (state.activeAction) return;
  const failedJob = state.jobs.find((job) => job.id === jobID && job.status === "failed");
  if (!failedJob) {
    throw new Error("Only failed Video Indexer jobs can be retried.");
  }
  state.activeAction = { kind: "retry", jobID };
  state.message = "Retrying the Video Indexer job with a fresh source URL...";
  try {
    const job = await VideoIndexerStudioService.SubmitForIndexing(failedJob.assetId);
    if (!job) {
      throw new Error("Video Indexer did not return the replacement job.");
    }
    upsertJob(state, job);
    state.selectedJobID = job.id;
  } finally {
    state.activeAction = null;
  }
}

export async function cancelVideoIndexerJob(state: VideoIndexerStudioViewState, jobID: string): Promise<void> {
  if (state.activeAction) return;
  state.activeAction = { kind: "cancel", jobID };
  state.message = "Canceling the Video Indexer job...";
  try {
    const job = await VideoIndexerStudioService.CancelIndexing(jobID);
    if (job) {
      upsertJob(state, job);
    }
  } finally {
    state.activeAction = null;
  }
}

export async function createEditProjectFromVideoIndexerJob(
  state: VideoIndexerStudioViewState,
  jobID: string,
  suggestionID = "",
): Promise<EditingModels.EditProject | null> {
  if (state.activeAction) return null;
  state.activeAction = { kind: "create-project", jobID };
  state.message = "Creating the edit project...";
  try {
    const project = await VideoIndexerStudioService.CreateEditProject(jobID, suggestionID);
    if (project) {
      state.message = `Created edit project ${project.name || project.id}.`;
      const found = state.jobs.find((job) => job.id === jobID);
      if (found) {
        found.projectId = project.id;
        if (suggestionID.trim()) {
          found.suggestionId = suggestionID.trim();
        }
      }
    }
    return project ?? null;
  } finally {
    state.activeAction = null;
  }
}

export async function saveVideoIndexerSettings(
  state: VideoIndexerStudioViewState,
  endpoint: string,
  apiKey: string,
): Promise<void> {
  if (state.settings.saving) return;
  state.settings.saving = true;
  state.settings.message = "Saving Video Indexer Studio settings...";
  try {
    await SettingsService.SetVideoIndexerServiceEndpoint(endpoint, apiKey);
    state.settings.endpoint = endpoint.trim();
    state.settings.apiKey = "";
    state.settings.status = await SettingsService.GetVideoIndexerServiceStatus();
    state.settings.message = state.settings.status?.message || "Video Indexer Studio settings saved.";
  } catch (error) {
    state.settings.message = error instanceof Error ? error.message : "Saving Video Indexer Studio settings failed.";
  } finally {
    state.settings.saving = false;
  }
}

function renderAssetRows(state: VideoIndexerStudioViewState): string {
  const latest = latestJobByAsset(state.jobs);
  return state.assets.length
    ? state.assets
        .filter(assetEligible)
        .map((asset) => {
          const selected = state.selectedAssetIDs.includes(asset.id);
          const job = latest.get(asset.id);
          const tone = job ? localStatusTone(job.status) : "neutral";
          return `
            <tr>
              <td><input class="check" type="checkbox" data-action="video-indexer-toggle-asset" data-asset-id="${escapeHTML(asset.id)}" ${selected ? "checked" : ""} ${state.activeAction ? "disabled" : ""} aria-label="Select ${escapeHTML(asset.name || asset.id)}" /></td>
              <td><strong>${escapeHTML(asset.name || asset.id)}</strong><br><span class="muted">${escapeHTML(asset.cloudAssetId || "No cloud asset ID")}</span></td>
              <td>${badge(job ? stageLabel(job.status) : "Eligible", tone)}</td>
              <td class="muted">${escapeHTML(formatRelative(job?.updatedAt || asset.createdAt))}</td>
              <td><span class="path-cell">${escapeHTML(asset.id)}</span></td>
            </tr>`;
        })
        .join("")
    : `<tr><td colspan="5" class="empty-state">No eligible library assets yet.</td></tr>`;
}

function renderJobsTable(state: VideoIndexerStudioViewState): string {
  const busy = state.activeAction !== null;
  const rows = state.jobs.length
    ? state.jobs
        .map((job) => {
          const selected = state.selectedJobID === job.id;
          const asset = state.assets.find((item) => item.id === job.assetId);
          const sourceCount = job.assetIds?.length || 0;
          const label = job.composition
            ? `Combined edit (${sourceCount} video${sourceCount === 1 ? "" : "s"})`
            : asset?.name || job.assetName || job.assetId;
          return `
            <tr ${selected ? 'data-selected="true"' : ""}>
              <td><strong>${escapeHTML(label)}</strong>${job.composition ? `<br><span class="muted">${sourceCount} source analyses</span>` : ""}</td>
              <td>${badge(stageLabel(job.status), localStatusTone(job.status))}</td>
              <td class="muted">${escapeHTML(firstNonEmpty(job.stage, job.remoteStatus, "—"))}</td>
              <td class="muted">${escapeHTML(formatRelative(job.updatedAt || job.createdAt))}</td>
              <td>
                <div class="toolbar">
                  <button type="button" class="button secondary small" data-action="video-indexer-view-job" data-job-id="${escapeHTML(job.id)}">View</button>
                  ${
                    inFlight(job.status)
                      ? `<button type="button" class="button secondary small" data-action="video-indexer-cancel-job" data-job-id="${escapeHTML(job.id)}" ${busy ? "disabled" : ""} ${state.activeAction?.kind === "cancel" && state.activeAction.jobID === job.id ? 'aria-busy="true"' : ""}>${state.activeAction?.kind === "cancel" && state.activeAction.jobID === job.id ? "Canceling..." : "Cancel"}</button>`
                      : ""
                  }
                  ${
                    job.status === "failed" && !job.composition
                      ? `<button type="button" class="button small" data-action="video-indexer-retry-job" data-job-id="${escapeHTML(job.id)}" ${busy ? "disabled" : ""} ${state.activeAction?.kind === "retry" && state.activeAction.jobID === job.id ? 'aria-busy="true"' : ""}>${state.activeAction?.kind === "retry" && state.activeAction.jobID === job.id ? "Retrying..." : "Retry"}</button>`
                      : ""
                  }
                  ${renderEditProjectAction(job, state, "button small")}
                </div>
              </td>
            </tr>`;
        })
        .join("")
    : `<tr><td colspan="5" class="empty-state">No Video Indexer jobs yet.</td></tr>`;
  return `
    <section class="panel" aria-labelledby="smart-edit-jobs-title">
      <div class="panel-header">
        <div>
          <p class="eyebrow">Video Indexer jobs</p>
          <h3 id="smart-edit-jobs-title">Queued and completed jobs</h3>
        </div>
        ${badge(`${state.jobs.length} job${state.jobs.length === 1 ? "" : "s"}`, state.jobs.length ? "info" : "neutral")}
      </div>
      <div class="table-wrap">
        <table aria-label="Video Indexer jobs">
          <thead>
            <tr>
              <th>Asset</th>
              <th>Status</th>
              <th>Stage</th>
              <th>Updated</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>${rows}</tbody>
        </table>
      </div>
      <div class="detail-body">
        <p class="queue-message">${escapeHTML(state.message || "Submit selected or pending assets to start a smart-edit job.")}</p>
      </div>
    </section>`;
}

function renderSignals(signals?: VI.MediaSignals | null): string {
  if (!signals) {
    return `<div class="empty-state">No media signals were recorded for this job.</div>`;
  }
  const silenceRows = (signals.silences || [])
    .map(
      (silence) => `
        <div class="kv">
          <span>Silence</span>
          <strong>${escapeHTML(formatDurationNs(silence.start))} – ${escapeHTML(formatDurationNs(silence.end))}</strong>
        </div>`,
    )
    .join("");
  return `
    <div class="signal-grid">
      <div class="kv"><span>Source URL</span><strong>${escapeHTML(signals.sourceUrl || "—")}</strong></div>
      <div class="kv"><span>Duration</span><strong>${escapeHTML(formatDurationNs(signals.duration))}</strong></div>
      <div class="kv"><span>Video</span><strong>${signals.video?.present ? "Present" : "Absent"} · ${escapeHTML(signals.video?.codec || "—")} · ${signals.video?.width || 0}×${signals.video?.height || 0}</strong></div>
      <div class="kv"><span>Audio</span><strong>${signals.audio?.present ? "Present" : "Absent"} · ${escapeHTML(signals.audio?.codec || "—")} · ${signals.audio?.channels || 0} ch</strong></div>
      ${silenceRows}
    </div>`;
}

function renderSourceRefs(refs: VI.SourceRef[] | undefined): string {
  if (!refs || refs.length === 0) {
    return `<span class="muted">No source refs</span>`;
  }
  return refs
    .map(
      (ref) => {
        const detail = [
          firstNonEmpty(ref.sourceKind, "source"),
          firstNonEmpty(ref.sourceAssetId, "asset"),
          ref.startMs === undefined && ref.endMs === undefined ? "" : `${formatMs(ref.startMs)} - ${formatMs(ref.endMs)}`,
          ref.text || "",
        ].filter(Boolean).join(" · ");
        return `
        <span class="source-ref" title="${escapeHTML(detail)}" aria-label="${escapeHTML(detail)}">
          ${escapeHTML(firstNonEmpty(ref.factKind, ref.sourceKind, ref.refId))}
        </span>`;
      },
    )
    .join("");
}

function canonicalCompositionDraft(job: BackendModels.VideoIndexerStudioJob): VI.TimelineDraft | null {
  const plan = job.compositionPlan;
  const drafts = job.timelineDrafts || [];
  const draft = drafts[0];
  const proposalClips = plan?.clips || [];
  const clips = draft?.primaryVideoTrack?.clips || [];
  if (!plan || plan.compositionId !== job.id || !proposalClips.length || !draft || drafts.length !== 1 || draft.originJobId !== job.id || clips.length !== proposalClips.length) return null;
  return clips.every((clip, index) => {
    const proposalClip = proposalClips[index];
    return proposalClip
      && clip.id === proposalClip.id
      && clip.sourceAssetId === proposalClip.sourceAssetId
      && clip.inMs === proposalClip.startMs
      && clip.outMs === proposalClip.endMs
      && clip.transition?.kind === "cut"
      && (clip.transition?.durationMs ?? 0) === 0;
  }) ? draft : null;
}

function legacyCompositionDraft(job: BackendModels.VideoIndexerStudioJob): VI.TimelineDraft | null {
  const drafts = job.timelineDrafts || [];
  const draft = drafts[0];
  const clips = draft?.primaryVideoTrack?.clips || [];
  if (job.status !== "succeeded" || job.compositionPlan || !draft || drafts.length !== 1 || draft.originJobId !== job.id || !clips.length) return null;
  return clips.every((clip) => clip.transition?.kind === "cut" && clip.transition?.durationMs === 0) ? draft : null;
}

function renderEditProjectAction(job: BackendModels.VideoIndexerStudioJob, state: VideoIndexerStudioViewState, className = "button", suggestionID = ""): string {
  if (job.projectId) {
    const opening = state.activeAction?.kind === "open-project" && state.activeAction.projectID === job.projectId;
    return `<button type="button" class="${className}" data-action="video-indexer-open-project" data-project-id="${escapeHTML(job.projectId)}" ${state.activeAction ? "disabled" : ""} ${opening ? 'aria-busy="true"' : ""}>${opening ? "Opening project..." : "Open in Editing"}</button>`;
  }
  const validDraft = job.composition
    ? canonicalCompositionDraft(job) ?? legacyCompositionDraft(job)
    : (job.timelineDrafts?.length || 0) === 1 ? job.timelineDrafts?.[0] ?? null : null;
  if (job.status !== "succeeded" || !validDraft) return "";
  const creating = state.activeAction?.kind === "create-project" && state.activeAction.jobID === job.id;
  const requestedSuggestionID = suggestionID || validDraft.suggestionId || "";
  return `<button type="button" class="${className}" data-action="video-indexer-create-project" data-job-id="${escapeHTML(job.id)}" data-suggestion-id="${escapeHTML(requestedSuggestionID)}" ${state.activeAction ? "disabled" : ""} ${creating ? 'aria-busy="true"' : ""}>${creating ? "Creating project..." : "Create edit project"}</button>`;
}
function renderTimelineDrafts(job: BackendModels.VideoIndexerStudioJob, state: VideoIndexerStudioViewState): string {
  const drafts = job.timelineDrafts || [];
  if (!drafts.length) {
    return `<div class="empty-state">No timeline drafts yet.</div>`;
  }
  return drafts
    .map((draft) => {
      const clips = draft.primaryVideoTrack?.clips || [];
      return `
        <article class="timeline-draft">
          <div class="panel-header">
            <div>
              <p class="eyebrow">Timeline draft</p>
              <h3>${escapeHTML(firstNonEmpty(draft.suggestionId, draft.promptVersion, "Draft"))}</h3>
            </div>
            <div class="toolbar">
              ${badge(`Schema ${draft.schemaVersion}`, "neutral")}
              ${renderEditProjectAction(job, state, "button", draft.suggestionId || "")}
            </div>
          </div>
          <div class="detail-body">
            <div class="kv"><span>Origin job</span><strong>${escapeHTML(firstNonEmpty(draft.originJobId, job.id))}</strong></div>
            <div class="kv"><span>Track</span><strong>${escapeHTML(firstNonEmpty(draft.primaryVideoTrack?.kind, "video"))}</strong></div>
            <div class="clip-list" aria-label="Ordered clip preview">
              ${
                clips.length
                  ? clips
                      .map(
                        (clip, index) => `
                          <div class="clip-preview">
                            <span class="badge neutral">${index + 1}</span>
                            <div>
                              <strong>${escapeHTML(firstNonEmpty(clip.id, `Clip ${index + 1}`))}</strong>
                              <p class="muted">${escapeHTML(formatMs(clip.inMs))} – ${escapeHTML(formatMs(clip.outMs))} · ${escapeHTML(firstNonEmpty(clip.transition?.kind, "cut"))}</p>
                              <p class="muted">${escapeHTML(firstNonEmpty(clip.sourceAssetId, job.assetId))}</p>
                            </div>
                          </div>`,
                      )
                      .join("")
                  : `<div class="empty-state">No clips were generated for this draft.</div>`
              }
            </div>
          </div>
        </article>`;
    })
    .join("");
}

function renderCompositionRecommendation(job: BackendModels.VideoIndexerStudioJob, state: VideoIndexerStudioViewState): string {
  if (!job.composition) return "";

  const plan = job.compositionPlan;
  const sources = compositionSourceStates(job, state);
  const planReady = job.status === "succeeded" && Boolean(plan);
  const statusMessage = job.status === "failed"
    ? firstNonEmpty(job.errorMessage, "One or more source analyses failed before a recommendation could be created.")
    : job.status === "canceled"
      ? "This composition was canceled. Submit a new composition when the source videos are ready."
    : job.status === "succeeded"
      ? "This older composition completed without a CompositionPlan. Its timeline draft can still be opened in Editing."
      : "Waiting for every selected source analysis to complete before ranking grounded clips.";

  return `
    <section class="panel composition-recommendation" aria-labelledby="composition-recommendation-title">
      <div class="panel-header">
        <div>
          <p class="eyebrow">Multi-video recommendation</p>
          <h3 id="composition-recommendation-title">${escapeHTML(plan?.title || "Composition in progress")}</h3>
        </div>
        <div class="toolbar">
          ${badge(planReady ? "Ready" : stageLabel(job.status), planReady ? "success" : localStatusTone(job.status))}
          ${plan ? badge(`${plan.clips.length} ranked clips`, "info") : ""}
        </div>
      </div>
      <div class="detail-body">
        ${planReady ? `
          <div class="composition-summary">
            <div class="kv"><span>Summary</span><strong>${escapeHTML(plan?.summary || "—")}</strong></div>
            <div class="kv"><span>Ranking</span><strong>${escapeHTML(plan?.rankingMode || "Grounded ranking")}</strong></div>
            <div class="kv"><span>Evidence</span><strong class="path-detail">${escapeHTML(plan?.evidenceFingerprint || "Unavailable")}</strong></div>
          </div>` : `<div class="empty-state"><strong>${job.status === "failed" ? "Composition unavailable" : job.status === "canceled" ? "Composition canceled" : "Composition not ready"}</strong><p>${escapeHTML(statusMessage)}</p></div>`}
        <div>
          <h4>Source analysis status</h4>
          ${sources.length ? `<div class="table-wrap"><table aria-label="Composition source analysis status"><thead><tr><th>Source</th><th>Analysis</th><th>Status</th><th>Duration</th></tr></thead><tbody>${sources.map((source) => {
            const assetName = state.assets.find((asset) => asset.id === source.assetID)?.name || source.assetID;
            return `<tr><td><strong>${escapeHTML(assetName)}</strong><br><span class="path-cell">${escapeHTML(source.assetID)}</span></td><td class="path-cell">${escapeHTML(source.analysisJobID || "Awaiting submission")}</td><td>${badge(compositionSourceStatusLabel(source.status), localStatusTone(source.status))}</td><td>${escapeHTML(source.durationMs > 0 ? formatMs(source.durationMs) : "—")}</td></tr>`;
          }).join("")}</tbody></table></div>` : `<div class="empty-state">Source analysis status has not been recorded for this legacy composition.</div>`}
        </div>
        ${!plan && legacyCompositionDraft(job) ? `<div class="edit-render-blocked"><strong>Composition provenance is unavailable for this older job.</strong><p>The backend retained one grounded, zero-duration-cut timeline draft. Create a persisted edit project from that draft, or request a new recommendation to receive composition provenance.</p></div>` : ""}
        ${planReady ? `<div>
          <h4>Ranked source clips</h4>
          ${plan?.clips.length ? `<ol class="composition-clip-list">${plan.clips.map((clip, index) => {
            const assetName = state.assets.find((asset) => asset.id === clip.sourceAssetId)?.name || clip.sourceAssetId;
            return `<li class="composition-clip"><span class="composition-rank">${index + 1}</span><div><div class="composition-clip-heading"><strong>${escapeHTML(clip.title || `Clip ${index + 1}`)}</strong><span>${escapeHTML(formatScore(clip.score))}</span></div><p>${escapeHTML(clip.reason || "No rationale was recorded.")}</p><dl><div><dt>Source</dt><dd>${escapeHTML(assetName)}</dd></div><div><dt>Range</dt><dd>${escapeHTML(formatMs(clip.startMs))} - ${escapeHTML(formatMs(clip.endMs))}</dd></div><div><dt>Suggestion</dt><dd>${escapeHTML(clip.suggestionId || "—")}</dd></div></dl><div class="source-chip-list">${renderSourceRefs(clip.sourceRefs)}</div></div></li>`;
          }).join("")}</ol>` : `<div class="empty-state">The completed CompositionPlan contains no ranked clips.</div>`}
        </div>
        <div class="composition-actions">${renderEditProjectAction(job, state)}</div>
        <p class="queue-message">The Editing workspace persists this sequence. Use its existing move-earlier, move-later, and remove controls to revise the saved project.</p>` : ""}
      </div>
    </section>`;
}

function renderJobDetails(job: BackendModels.VideoIndexerStudioJob | null, state: VideoIndexerStudioViewState): string {
  if (!job) {
    return `
      <section class="panel" aria-labelledby="smart-edit-detail-title">
        <div class="panel-header">
          <div>
            <p class="eyebrow">Job details</p>
            <h3 id="smart-edit-detail-title">Select a job</h3>
          </div>
        </div>
        <div class="detail-body">
          <div class="empty-state">Choose a Video Indexer job to inspect scenes, transcript, OCR, labels, objects, highlights, suggestions, and timeline drafts.</div>
        </div>
      </section>`;
  }

  const result = job.videoIndexResult;
  const editPlan = job.editPlan;
  const latestAsset = state.assets.find((asset) => asset.id === job.assetId);
  const selectedJobTone = localStatusTone(job.status);
  const sourceAssets = (job.assetIds || []).map((assetID) => state.assets.find((asset) => asset.id === assetID)?.name || assetID);
  const jobTitle = job.composition
    ? `Combined edit (${sourceAssets.length} videos)`
    : job.assetName || latestAsset?.name || job.assetId;
  return `
    <section class="panel" aria-labelledby="smart-edit-detail-title">
      <div class="panel-header">
        <div>
          <p class="eyebrow">Job details</p>
          <h3 id="smart-edit-detail-title">${escapeHTML(jobTitle)}</h3>
        </div>
        <div class="toolbar">
          ${badge(stageLabel(job.status), selectedJobTone)}
          ${job.retryable ? badge("Retryable failure", "warning") : ""}
        </div>
      </div>
      <div class="detail-body">
        <div class="kv"><span>${job.composition ? "Source videos" : "Asset"}</span><strong>${escapeHTML(job.composition ? sourceAssets.join(", ") : firstNonEmpty(latestAsset?.name, job.assetName, job.assetId))}</strong></div>
        ${job.composition ? `<div class="kv"><span>Analyses</span><strong>${job.dependencyJobIds?.length || 0} dependencies</strong></div>` : ""}
        <div class="kv"><span>Status</span><strong>${escapeHTML(job.status)}</strong></div>
        <div class="kv"><span>Stage</span><strong>${escapeHTML(firstNonEmpty(job.stage, job.remoteStatus, "—"))}</strong></div>
        <div class="kv"><span>Created</span><strong>${escapeHTML(formatDate(job.createdAt))}</strong></div>
        <div class="kv"><span>Updated</span><strong>${escapeHTML(formatDate(job.updatedAt))}</strong></div>
        <div class="kv"><span>Error</span><strong>${escapeHTML(job.errorMessage || "—")}</strong></div>
      </div>
    </section>

    ${renderCompositionRecommendation(job, state)}

    <section class="panel">
      <div class="panel-header">
        <div>
          <p class="eyebrow">Checkpoints</p>
          <h3>Stage history</h3>
        </div>
      </div>
      <div class="detail-body">
        ${
          job.checkpoints?.length
            ? job.checkpoints
                .map(
                  (checkpoint) => `
                    <div class="checkpoint-row">
                      ${badge(firstNonEmpty(checkpoint.stage, "Checkpoint"), "neutral")}
                      <div>
                        <strong>${escapeHTML(firstNonEmpty(checkpoint.state, checkpoint.detail, "—"))}</strong>
                        <p class="muted">${escapeHTML(formatDate(checkpoint.at))}${checkpoint.videoId ? ` · ${escapeHTML(checkpoint.videoId)}` : ""}</p>
                      </div>
                    </div>`,
                )
                .join("")
            : `<div class="empty-state">No checkpoints are stored for this job yet.</div>`
        }
      </div>
    </section>

    ${!job.composition ? `<section class="panel">
      <div class="panel-header">
        <div>
          <p class="eyebrow">Video index result</p>
          <h3>Scenes, shots, transcript, OCR, labels, objects</h3>
        </div>
      </div>
      <div class="detail-body">
        ${
          result
            ? `
              <div class="signal-grid">
                <div class="kv"><span>Video ID</span><strong>${escapeHTML(result.videoId || "—")}</strong></div>
                <div class="kv"><span>Duration</span><strong>${escapeHTML(formatMs(result.durationMs))}</strong></div>
                <div class="kv"><span>Detected language</span><strong>${escapeHTML(firstNonEmpty(result.detectedLanguage, result.sourceLanguage, "—"))}</strong></div>
                <div class="kv"><span>Source IDs</span><strong>${escapeHTML((result.sourceIds || []).join(", ") || "—")}</strong></div>
              </div>
              <div class="detail-stack">
                <div>
                  <h4>Media signals</h4>
                  ${renderSignals(result.technicalSignals)}
                </div>
                <div>
                  <h4>Scenes</h4>
                  ${
                    result.insights?.scenes?.length
                      ? `
                        <table>
                          <thead><tr><th>Time</th><th>Thumbnail</th><th>Confidence</th></tr></thead>
                          <tbody>
                            ${result.insights.scenes
                              .map(
                                (scene) => `
                                  <tr>
                                    <td>${escapeHTML(formatMs(scene.startMs))} – ${escapeHTML(formatMs(scene.endMs))}</td>
                                    <td class="muted">${escapeHTML(firstNonEmpty(scene.thumbnail?.thumbnailId, scene.thumbnail?.url, scene.id))}</td>
                                    <td>${Math.round((scene.confidence || 0) * 100)}%</td>
                                  </tr>`,
                              )
                              .join("")}
                          </tbody>
                        </table>`
                      : `<div class="empty-state">No scenes available.</div>`
                  }
                </div>
                <div>
                  <h4>Shots</h4>
                  ${
                    result.insights?.shots?.length
                      ? `
                        <table>
                          <thead><tr><th>Time</th><th>Keyframes</th><th>Tags</th></tr></thead>
                          <tbody>
                            ${result.insights.shots
                              .map(
                                (shot) => `
                                  <tr>
                                    <td>${escapeHTML(formatMs(shot.startMs))} – ${escapeHTML(formatMs(shot.endMs))}</td>
                                    <td class="muted">${escapeHTML((shot.keyframeIds || []).join(", ") || "—")}</td>
                                    <td class="muted">${escapeHTML((shot.tags || []).join(", ") || "—")}</td>
                                  </tr>`,
                              )
                              .join("")}
                          </tbody>
                        </table>`
                      : `<div class="empty-state">No shots available.</div>`
                  }
                </div>
                <div>
                  <h4>Transcript and speakers</h4>
                  ${
                    result.insights?.transcript?.length
                      ? `
                        <table>
                          <thead><tr><th>Time</th><th>Speaker</th><th>Text</th></tr></thead>
                          <tbody>
                            ${result.insights.transcript
                              .map(
                                (item) => `
                                  <tr>
                                    <td>${escapeHTML(formatMs(item.startMs))} – ${escapeHTML(formatMs(item.endMs))}</td>
                                    <td class="muted">${escapeHTML(firstNonEmpty(item.speakerName, item.speakerId, "—"))}</td>
                                    <td>${escapeHTML(item.text || "—")}</td>
                                  </tr>`,
                              )
                              .join("")}
                          </tbody>
                        </table>`
                      : `<div class="empty-state">No transcript available.</div>`
                  }
                  ${
                    result.insights?.speakers?.length
                      ? `
                        <div class="source-chip-list">
                          ${result.insights.speakers
                            .map((speaker) => badge(firstNonEmpty(speaker.name, speaker.id), "info"))
                            .join("")}
                        </div>`
                      : ""
                  }
                </div>
                <div>
                  <h4>OCR, labels, and objects</h4>
                  <div class="split-columns">
                    <div>
                      <h5>OCR</h5>
                      ${
                        result.insights?.ocr?.length
                          ? result.insights.ocr
                              .map(
                                (item) => `
                                  <div class="kv">
                                    <span>${escapeHTML(formatMs(item.startMs))}</span>
                                    <strong>${escapeHTML(item.text || "—")}</strong>
                                  </div>`,
                              )
                              .join("")
                          : `<div class="empty-state">No OCR text.</div>`
                      }
                    </div>
                    <div>
                      <h5>Labels</h5>
                      ${
                        result.insights?.labels?.length
                          ? result.insights.labels
                              .map(
                                (item) => `
                                  <div class="kv">
                                    <span>${escapeHTML(formatMs(item.startMs))}</span>
                                    <strong>${escapeHTML(item.name || "—")}</strong>
                                  </div>`,
                              )
                              .join("")
                          : `<div class="empty-state">No labels.</div>`
                      }
                    </div>
                    <div>
                      <h5>Objects</h5>
                      ${
                        result.insights?.objects?.length
                          ? result.insights.objects
                              .map(
                                (item) => `
                                  <div class="kv">
                                    <span>${escapeHTML(formatMs(item.startMs))}</span>
                                    <strong>${escapeHTML(firstNonEmpty(item.displayName, item.type, item.id))}</strong>
                                  </div>`,
                              )
                              .join("")
                          : `<div class="empty-state">No detected objects.</div>`
                      }
                    </div>
                  </div>
                </div>
              </div>`
            : `<div class="empty-state">No detailed result payload is attached to this job yet.</div>`
        }
      </div>
    </section>` : ""}

    ${!job.composition ? `<section class="panel">
      <div class="panel-header">
        <div>
          <p class="eyebrow">Grounded edit plan</p>
          <h3>Highlights and ordered suggestions</h3>
        </div>
      </div>
      <div class="detail-body">
        ${
          editPlan
            ? `
              <div class="kv"><span>Title</span><strong>${escapeHTML(editPlan.title || "—")}</strong></div>
              <div class="kv"><span>Summary</span><strong>${escapeHTML(editPlan.summary || "—")}</strong></div>
              <div class="detail-stack">
                <div>
                  <h4>Highlights</h4>
                  ${
                    editPlan.highlights?.length
                      ? editPlan.highlights
                          .map(
                            (highlight) => `
                              <article class="suggestion-card">
                                <h4>${escapeHTML(highlight.title || highlight.id)}</h4>
                                <p>${escapeHTML(highlight.reason || "—")}</p>
                                <p class="muted">${escapeHTML(formatMs(highlight.startMs))} – ${escapeHTML(formatMs(highlight.endMs))} · ${Math.round((highlight.score || 0) * 100)}%</p>
                                <div class="source-chip-list">${renderSourceRefs(highlight.sourceRefs)}</div>
                              </article>`,
                          )
                          .join("")
                      : `<div class="empty-state">No grounded highlights available.</div>`
                  }
                </div>
                <div>
                  <h4>Suggestions</h4>
                  ${
                    editPlan.suggestions?.length
                      ? editPlan.suggestions
                          .map(
                            (suggestion) => `
                              <article class="suggestion-card">
                                <div class="toolbar">
                                  <h4>${escapeHTML(suggestion.title || suggestion.id)}</h4>
                                  <button type="button" class="button small" data-action="video-indexer-create-project" data-job-id="${escapeHTML(job.id)}" data-suggestion-id="${escapeHTML(suggestion.id)}">Create edit project</button>
                                </div>
                                <p>${escapeHTML(suggestion.reason || "—")}</p>
                                <p class="muted">${escapeHTML(formatMs(suggestion.startMs))} – ${escapeHTML(formatMs(suggestion.endMs))} · ${Math.round((suggestion.score || 0) * 100)}%</p>
                                <div class="source-chip-list">${renderSourceRefs(suggestion.sourceRefs)}</div>
                                ${
                                  suggestion.clips?.length
                                    ? `<ol class="ordered-clips">
                                        ${suggestion.clips
                                          .map(
                                            (clip) => `
                                              <li>
                                                <strong>${escapeHTML(clip.title || clip.id)}</strong>
                                                <p>${escapeHTML(formatMs(clip.startMs))} – ${escapeHTML(formatMs(clip.endMs))} · ${escapeHTML(clip.reason || "—")}</p>
                                              </li>`,
                                          )
                                          .join("")}
                                      </ol>`
                                    : ""
                                }
                              </article>`,
                          )
                          .join("")
                      : `<div class="empty-state">No edit suggestions available.</div>`
                  }
                </div>
              </div>`
            : `<div class="empty-state">No grounded edit plan has been attached yet.</div>`
        }
      </div>
    </section>` : ""}

    <section class="panel">
      <div class="panel-header">
        <div>
          <p class="eyebrow">Timeline draft preview</p>
          <h3>Non-destructive output preview</h3>
        </div>
      </div>
      <div class="detail-body">
        ${renderTimelineDrafts(job, state)}
      </div>
    </section>
  `;
}

export function renderVideoIndexerStudioPanel(state: VideoIndexerStudioViewState): string {
  const selectedAssetsList = selectedAssets(state);
  const selectedEligibleAssets = selectedAssetsList.filter(assetEligible);
  const pending = pendingAssets(state);
  const selectedJob = state.jobs.find((job) => job.id === state.selectedJobID) || state.jobs[0] || null;
  const action = state.activeAction;
  const busy = action !== null;
  const submittingSelected = action?.kind === "submit-selected";
  const submittingPending = action?.kind === "submit-pending";
  const refreshing = action?.kind === "refresh";
  const generatingComposition = action?.kind === "generate-composition";
  const selectedSubmissionCount = action?.kind === "submit-selected" ? action.count : 0;
  const pendingSubmissionCount = action?.kind === "submit-pending" ? action.count : 0;
  return `
    <section class="panel" aria-labelledby="smart-edit-title">
      <div class="panel-header">
        <div>
          <p class="eyebrow">Azure AI Video Indexer</p>
          <h3 id="smart-edit-title">Smart Edit Studio</h3>
        </div>
        <div class="toolbar">
          ${badge(`${state.jobs.length} job${state.jobs.length === 1 ? "" : "s"}`, state.jobs.length ? "info" : "neutral")}
          ${badge(`${pending.length} pending`, pending.length ? "warning" : "neutral")}
        </div>
      </div>
      <div class="detail-body">
        <div class="toolbar">
          <button type="button" class="button" data-action="video-indexer-generate-composition" ${selectedEligibleAssets.length >= 2 && !busy ? "" : "disabled"} ${generatingComposition ? 'aria-busy="true"' : ""}>${generatingComposition ? `Preparing ${action.count} videos...` : `Generate combined edit${selectedEligibleAssets.length >= 2 ? ` (${selectedEligibleAssets.length})` : ""}`}</button>
          <button type="button" class="button" data-action="video-indexer-submit-selected" ${selectedAssetsList.length && !busy ? "" : "disabled"} ${submittingSelected ? 'aria-busy="true"' : ""}>${submittingSelected ? `Submitting ${selectedSubmissionCount}...` : "Submit selected"}</button>
          <button type="button" class="button secondary" data-action="video-indexer-submit-pending" ${pending.length && !busy ? "" : "disabled"} ${submittingPending ? 'aria-busy="true"' : ""}>${submittingPending ? `Submitting ${pendingSubmissionCount}...` : "Submit pending"}</button>
          <button type="button" class="button secondary" data-action="video-indexer-refresh" ${busy ? "disabled" : ""} ${refreshing ? 'aria-busy="true"' : ""}>${refreshing ? "Refreshing..." : "Refresh"}</button>
        </div>
        <p class="queue-message" role="status" aria-live="polite">${escapeHTML(state.message || "Select eligible library assets to submit them to Video Indexer.")}</p>
      </div>
    </section>

    <section class="panel">
      <div class="panel-header">
        <div>
          <p class="eyebrow">Eligible assets</p>
          <h3>Library assets ready for indexing</h3>
        </div>
        ${badge(`${selectedAssetsList.length} selected`, selectedAssetsList.length ? "success" : "neutral")}
      </div>
      <div class="table-wrap">
        <table aria-label="Eligible assets">
          <thead>
            <tr>
              <th><span class="muted">Select</span></th>
              <th>Asset</th>
              <th>Current state</th>
              <th>Updated</th>
              <th>Asset ID</th>
            </tr>
          </thead>
          <tbody>${renderAssetRows(state)}</tbody>
        </table>
      </div>
    </section>

    ${renderJobsTable(state)}
    ${renderJobDetails(selectedJob, state)}
  `;
}

export function renderVideoIndexerSettingsCard(state: VideoIndexerStudioViewState): string {
  const status = state.settings.status;
  return `
    <section class="panel" aria-labelledby="smart-edit-settings-title">
      <div class="panel-header">
        <div>
          <p class="eyebrow">Smart Edit Studio settings</p>
          <h3 id="smart-edit-settings-title">Video Indexer connection</h3>
        </div>
        ${badge(status?.configured ? "Configured" : "Not configured", status?.configured ? "success" : "warning")}
      </div>
      <div class="detail-body">
        <div class="settings-grid">
          <label>
            <span>Video Indexer endpoint</span>
            <input class="field" data-setting="video-indexer-endpoint" value="${escapeHTML(state.settings.endpoint || "")}" placeholder="https://video-indexer.example" aria-label="Video Indexer endpoint" />
          </label>
          <label>
            <span>Video Indexer API key</span>
            <input class="field" data-setting="video-indexer-apikey" type="password" value="" placeholder="Paste to replace the stored key" autocomplete="new-password" aria-label="Video Indexer API key" />
          </label>
        </div>
        <div class="kv"><span>Status</span><strong>${escapeHTML(status?.message || state.settings.message || "Not configured")}</strong></div>
        <div class="kv"><span>Configured endpoint</span><strong>${escapeHTML(status?.endpoint || state.settings.endpoint || "—")}</strong></div>
        <div class="kv"><span>Has API key</span><strong>${status?.hasApiKey ? "Yes" : "No"}</strong></div>
        <div class="actions">
          <button type="button" class="button secondary" data-action="video-indexer-save-settings" ${state.settings.saving ? "disabled" : ""}>${state.settings.saving ? "Saving..." : "Save Video Indexer settings"}</button>
        </div>
        <p class="queue-message">${escapeHTML(state.settings.message || "Updating the API key replaces the stored secret; leaving it blank keeps the current value.")}</p>
      </div>
    </section>
  `;
}

let pollTimer: number | undefined;

export function setupVideoIndexerStudioEvents(state: VideoIndexerStudioViewState, onChange: () => void = () => {}): () => void {
  const offProgress = Events.On("video-indexer:progress", (event) => {
    const job = BackendModels.VideoIndexerStudioJob.createFrom(event.data as Partial<BackendModels.VideoIndexerStudioJob>);
    upsertJob(state, job);
    onChange();
  });
  const offCompleted = Events.On("video-indexer:completed", (event) => {
    const job = BackendModels.VideoIndexerStudioJob.createFrom(event.data as Partial<BackendModels.VideoIndexerStudioJob>);
    upsertJob(state, job);
    onChange();
  });
  const offFailed = Events.On("video-indexer:failed", (event) => {
    const job = BackendModels.VideoIndexerStudioJob.createFrom(event.data as Partial<BackendModels.VideoIndexerStudioJob>);
    upsertJob(state, job);
    onChange();
  });

  if (pollTimer !== undefined) {
    window.clearInterval(pollTimer);
  }
  pollTimer = window.setInterval(() => {
    if (!state.polling) return;
    void refreshVideoIndexerStudioState(state).then(onChange);
  }, 5000);

  return () => {
    offProgress();
    offCompleted();
    offFailed();
    if (pollTimer !== undefined) {
      window.clearInterval(pollTimer);
      pollTimer = undefined;
    }
  };
}
