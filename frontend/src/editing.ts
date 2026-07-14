// Editing Studio — non-destructive timeline editor with Azure Container App
// render pipeline. Drag analyzed assets into the timeline, trim, reorder,
// add transitions, and render to OneDrive.

import { Events } from "@wailsio/runtime";
import {
  EditingService,
  ProjectLibraryService,
} from "../bindings/github.com/zecloud/ai-video-studio/internal/backend/index.js";
import {
  EditProject,
  ClipSegment,
  Timeline,
  Track,
  RenderJob,
} from "../bindings/github.com/zecloud/ai-video-studio/internal/editing/models.js";
import { ProjectAsset } from "../bindings/github.com/zecloud/ai-video-studio/internal/library/models.js";

// ---- State ----

export interface EditingViewState {
  projects: EditProject[];
  activeProject: EditProject | null;
  assets: ProjectAsset[];
  renderJob: RenderJob | null;
  renderInFlight: boolean;
  message: string;
}

export function createEditingState(): EditingViewState {
  return {
    projects: [],
    activeProject: null,
    assets: [],
    renderJob: null,
    renderInFlight: false,
    message: "",
  };
}

// ---- Helpers ----

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

function msToDisplay(ms: number): string {
  const totalSeconds = Math.floor(ms / 1000);
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  return `${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")}`;
}

function durationDisplay(inMs: number, outMs: number): string {
  const dur = outMs > 0 ? outMs - inMs : 0;
  if (dur <= 0) return msToDisplay(inMs);
  return `${Math.round(dur / 1000)}s`;
}

// ---- Data loading ----

export async function loadEditingData(state: EditingViewState): Promise<void> {
  try {
    const [projects, assets] = await Promise.all([
      EditingService.ListProjects(),
      ProjectLibraryService.ListAssets(),
    ]);
    state.projects = projects ?? [];
    state.assets = assets ?? [];

    if (state.activeProject && state.projects.length > 0) {
      const found = state.projects.find((p) => p.id === state.activeProject!.id);
          state.activeProject = found ?? state.projects[0]!;
    } else if (state.projects.length > 0 && !state.activeProject) {
          state.activeProject = state.projects[0]!;
    }
  } catch (err) {
    state.message = `Failed to load projects: ${String(err)}`;
  }
}

// ---- Actions ----

export async function createNewProject(state: EditingViewState, name: string): Promise<void> {
  try {
    const project = await EditingService.CreateDraftProject(name);
    state.projects.push(project);
    state.activeProject = project;
  } catch (err) {
    state.message = `Failed to create project: ${String(err)}`;
  }
}

export async function saveProject(state: EditingViewState): Promise<void> {
  if (!state.activeProject) return;
  try {
    const saved = await EditingService.SaveProject(state.activeProject);
    const idx = state.projects.findIndex((p) => p.id === saved.id);
    if (idx >= 0) state.projects[idx] = saved;
    state.activeProject = saved;
    state.message = "Project saved.";
  } catch (err) {
    state.message = `Failed to save project: ${String(err)}`;
  }
}

export async function addClipToTimeline(
  state: EditingViewState,
  asset: ProjectAsset,
): Promise<void> {
  const project = state.activeProject;
  if (!project) return;

  const timeline = project.timeline ?? new Timeline({ tracks: [] });
  let videoTrack = timeline.tracks.find((t) => t.kind === "video");
  if (!videoTrack) {
    videoTrack = new Track({ id: "video-1", kind: "video", clips: [] });
    timeline.tracks.push(videoTrack);
  }

  const clip = new ClipSegment({
    id: `clip-${Date.now()}-${Math.random().toString(36).slice(2, 6)}`,
    sourceAssetId: asset.id,
    inMs: 0,
    outMs: 0,
  });
  videoTrack.clips.push(clip);
  project.timeline = timeline;
}

export function removeClip(state: EditingViewState, clipID: string): void {
  const project = state.activeProject;
  if (!project) return;
  for (const track of project.timeline.tracks) {
    track.clips = track.clips.filter((c) => c.id !== clipID);
  }
}

export function moveClipUp(state: EditingViewState, clipID: string): void {
  const project = state.activeProject;
  if (!project) return;
  for (const track of project.timeline.tracks) {
    const idx = track.clips.findIndex((c) => c.id === clipID);
    if (idx > 0) {
      const removed = track.clips.splice(idx, 1);
      if (removed.length > 0) {
        track.clips.splice(idx - 1, 0, removed[0]!);
      }
      return;
    }
  }
}

export function moveClipDown(state: EditingViewState, clipID: string): void {
  const project = state.activeProject;
  if (!project) return;
  for (const track of project.timeline.tracks) {
    const idx = track.clips.findIndex((c) => c.id === clipID);
    if (idx >= 0 && idx < track.clips.length - 1) {
      const removed = track.clips.splice(idx, 1);
      if (removed.length > 0) {
        track.clips.splice(idx + 1, 0, removed[0]!);
      }
      return;
    }
  }
}

export async function startRender(state: EditingViewState): Promise<void> {
  if (!state.activeProject) return;
  state.renderInFlight = true;
  state.message = "Render submitted to Azure Container App...";
  try {
    const job = await EditingService.Render(state.activeProject.id);
    state.renderJob = job ?? null;
    state.renderInFlight = false;
    if (job && job.status === "completed") {
      state.message = `Render complete! Output: ${job.outputUrl || "uploaded to OneDrive"}`;
    } else if (job && job.status === "failed") {
      state.message = `Render failed: ${job.errorDetail || "unknown error"}`;
    } else {
      state.message = "Render submitted. Check progress in the render panel.";
    }
  } catch (err) {
    state.renderInFlight = false;
    if (state.renderJob?.status === "canceled" || state.renderJob?.status === "cancellation_requested") {
      state.message = "Render canceled.";
    } else {
      state.message = `Render error: ${String(err)}`;
    }
  }
}

export async function cancelRender(state: EditingViewState): Promise<void> {
  const job = state.renderJob;
  if (!job || !state.renderInFlight) return;
  state.message = "Requesting render cancellation...";
  try {
    const canceled = await EditingService.CancelRender(job.id);
    if (canceled) state.renderJob = canceled;
  } catch (err) {
    state.message = `Cancellation error: ${String(err)}`;
  }
}

// ---- Event handling ----

export function setupEditingEvents(
  state: EditingViewState,
  reRender: () => void,
): () => void {
  const unsubProgress = Events.On("editing:render:progress", (ev) => {
    const job = RenderJob.createFrom(ev.data as Partial<RenderJob>);
    state.renderJob = job;
    reRender();
  });
  const unsubCompleted = Events.On("editing:render:completed", (ev) => {
    const job = RenderJob.createFrom(ev.data as Partial<RenderJob>);
    state.renderJob = job;
    state.renderInFlight = false;
    state.message = job.status === "completed" ? "Render uploaded to OneDrive." : job.status === "canceled" ? "Render canceled." : job.message || state.message;
    reRender();
  });
  return () => {
    unsubProgress();
    unsubCompleted();
  };
}

// ---- Rendering ----

export function renderEditingPanel(state: EditingViewState): string {
  const project = state.activeProject;

  // ---- Left: Asset picker ----
  const analyzedAssets = state.assets.filter(
    (a) => a.analysisJobId && a.analysisStatus === "succeeded",
  );

  const assetCards =
    analyzedAssets.length === 0
      ? `<div class="empty-assets">No analyzed assets yet.<br>Analyze videos from the Library first.</div>`
      : analyzedAssets
          .map(
            (a) => `
            <div class="asset-card" data-action="add-asset" data-asset-id="${escapeHTML(a.id)}">
              <div class="name">${escapeHTML(a.name || a.id)}</div>
              <div class="meta">${badge("analyzed", "success")}</div>
            </div>`,
          )
          .join("");

  // ---- Center: Timeline ----
  const clips = project
    ? project.timeline.tracks.flatMap((t) => t.clips)
    : [];

  const clipRows =
    clips.length === 0
      ? `<div class="empty-timeline">No clips on the timeline yet.<br>Click an analyzed asset to add it here.</div>`
      : clips
          .map(
            (c, i) => {
              const asset = state.assets.find((a) => a.id === c.sourceAssetId);
              const name = asset?.name ?? c.sourceAssetId ?? "Unknown";
              return `
            <div class="timeline-clip" data-clip-id="${escapeHTML(c.id)}">
              <span class="clip-name">${escapeHTML(name)}</span>
              <span class="clip-range">${msToDisplay(c.inMs)} – ${c.outMs > 0 ? msToDisplay(c.outMs) : "end"}</span>
              <span class="badge neutral">${durationDisplay(c.inMs, c.outMs)}</span>
              <div class="clip-actions">
                <button class="button secondary small" data-action="move-up" data-clip-id="${escapeHTML(c.id)}" ${i === 0 ? "disabled" : ""}>▲</button>
                <button class="button secondary small" data-action="move-down" data-clip-id="${escapeHTML(c.id)}" ${i === clips.length - 1 ? "disabled" : ""}>▼</button>
                <button class="button danger small" data-action="remove-clip" data-clip-id="${escapeHTML(c.id)}">✕</button>
              </div>
            </div>`;
            },
          )
          .join("");

  // ---- Right: Properties + Render ----
  const renderSection = buildRenderSection(state);

  // Build the full 3-column layout.
  return `
    <div class="editing-shell">
      <div class="asset-picker">
        <div class="asset-picker-header">
          <h3>Analyzed assets</h3>
          <span class="badge neutral">${analyzedAssets.length} ready</span>
        </div>
        <div class="asset-list">${assetCards}</div>
      </div>

      <div class="timeline-editor">
        <div class="timeline-toolbar">
          <select class="preset-select" data-action="set-preset">
            <option value="h264-1080p" ${project?.renderPreset === "h264-1080p" || !project?.renderPreset ? "selected" : ""}>H.264 1080p (Web)</option>
            <option value="h264-720p" ${project?.renderPreset === "h264-720p" ? "selected" : ""}>H.264 720p (Fast)</option>
            <option value="h265-1080p" ${project?.renderPreset === "h265-1080p" ? "selected" : ""}>H.265 1080p (Efficient)</option>
          </select>
          <button class="button secondary small" data-action="save-project">Save project</button>
          <span style="flex:1"></span>
          ${state.renderInFlight && state.renderJob && state.renderJob.status !== "cancellation_requested" ? `<button class="button danger" data-action="cancel-render">Cancel render</button>` : ""}
          <button class="button" data-action="start-render" ${state.renderInFlight ? "disabled" : ""} style="background:#15803d">
            ${state.renderInFlight ? "Rendering..." : "► Render"}
          </button>
        </div>
        <div class="timeline-canvas">${clipRows}</div>
        <div class="timeline-summary">
          <span>${clips.length} clip${clips.length !== 1 ? "s" : ""} on timeline</span>
          <span>Project: ${escapeHTML(project?.name ?? "—")}</span>
        </div>
      </div>

      <div class="properties-panel">
        ${renderSection}
        <div class="project-history">
          <h3>Projects</h3>
          ${state.projects
            .map(
              (p) => `
            <div class="project-item" data-action="select-project" data-project-id="${escapeHTML(p.id)}" style="${p.id === project?.id ? "border-color:var(--accent);background:var(--accent-soft)" : ""}">
              <div style="font-weight:700">${escapeHTML(p.name)}</div>
              <div class="pj-date">${p.renderPreset ? `Preset: ${escapeHTML(p.renderPreset)}` : "Draft"}</div>
            </div>`,
            )
            .join("")}
          <button class="button secondary small" data-action="new-project" style="margin-top:8px;width:100%">+ New project</button>
        </div>
      </div>
    </div>
  `;
}

function buildRenderSection(state: EditingViewState): string {
  const job = state.renderJob;

  let progressHTML = "";
  if (state.renderInFlight) {
    const percent = Math.max(0, Math.min(100, job?.percent ?? 0));
    progressHTML = `
      <div class="render-progress">
        <div class="bar"><span style="width:${percent}%"></span></div>
        <div class="progress-meta"><span>${escapeHTML(job?.message || "Submitting render job...")}</span><span>${Math.round(percent)}%</span></div>
      </div>`;
  } else if (job) {
    const statusTone = job.status === "completed" ? "success" : job.status === "failed" ? "danger" : job.status === "canceled" ? "warning" : "info";
    progressHTML = `
      <div class="render-progress">
        <p>${badge(job.status, statusTone)}</p>
        ${job.outputUrl ? `<p class="muted" style="margin-top:6px;font-size:0.78rem;word-break:break-all">Output: ${escapeHTML(job.outputUrl)}</p>` : ""}
        ${job.errorDetail ? `<p style="margin-top:6px;font-size:0.76rem;color:var(--danger)">${escapeHTML(job.errorDetail)}</p>` : ""}
      </div>`;
  } else {
    progressHTML = `<p class="muted" style="font-size:0.8rem">No render jobs yet.</p>`;
  }

  return `
    <div class="render-section">
      <h3>Render pipeline</h3>
      <p style="font-size:0.76rem;color:var(--ink-muted);margin-bottom:10px">Dispatched to Azure Container App with FFmpeg.</p>
      ${progressHTML}
    </div>
    <div class="props-section">
      <h3>Clip properties</h3>
      <p class="muted" style="font-size:0.8rem">Select a clip to edit trim points and transitions.</p>
    </div>
  `;
}