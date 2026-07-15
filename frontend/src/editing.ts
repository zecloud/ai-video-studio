import { Events } from "@wailsio/runtime";
import { EditingService, ProjectLibraryService } from "../bindings/github.com/zecloud/ai-video-studio/internal/backend/index.js";
import { EditingCapabilities } from "../bindings/github.com/zecloud/ai-video-studio/internal/backend/models.js";
import { EditProject, RenderJob } from "../bindings/github.com/zecloud/ai-video-studio/internal/editing/models.js";
import { ProjectAsset } from "../bindings/github.com/zecloud/ai-video-studio/internal/library/models.js";

export interface EditingViewState {
  projects: EditProject[];
  activeProject: EditProject | null;
  assets: ProjectAsset[];
  capabilities: EditingCapabilities | null;
  renderJob: RenderJob | null;
  renderInFlight: boolean;
  selectedClipID: string | null;
  message: string;
}

export function createEditingState(): EditingViewState {
  return { projects: [], activeProject: null, assets: [], capabilities: null, renderJob: null, renderInFlight: false, selectedClipID: null, message: "" };
}

function escapeHTML(value: string): string {
  return (value ?? "").replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;").replaceAll('"', "&quot;");
}

function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "Size unavailable";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit += 1; }
  return `${value >= 10 || unit === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[unit]}`;
}

function videoClips(project: EditProject | null) {
  return project?.timeline?.tracks.filter((track) => !track.kind || track.kind === "video").flatMap((track) => track.clips ?? []) ?? [];
}

function isActiveRender(job: RenderJob | null): boolean {
  return Boolean(job && !["completed", "failed", "canceled"].includes(job.status));
}

function currentRenderJob(jobs: RenderJob[]): RenderJob | null {
  return jobs.find(isActiveRender) ?? jobs.at(-1) ?? null;
}

export async function loadEditingData(state: EditingViewState, activeProjectID?: string): Promise<void> {
  try {
    const [projects, assets, capabilities, jobs] = await Promise.all([
      EditingService.ListProjects(),
      ProjectLibraryService.ListAssets(),
      EditingService.Capabilities(),
      EditingService.RenderJobs(),
    ]);
    state.projects = projects ?? [];
    state.assets = assets ?? [];
    state.capabilities = capabilities;
    const requestedProject = activeProjectID ? state.projects.find((project) => project.id === activeProjectID) : null;
    const currentProject = state.activeProject ? state.projects.find((project) => project.id === state.activeProject!.id) : null;
    state.activeProject = requestedProject ?? currentProject ?? state.projects[0] ?? null;
    const currentClips = videoClips(state.activeProject);
    if (!currentClips.some((clip) => clip.id === state.selectedClipID)) state.selectedClipID = currentClips[0]?.id ?? null;
    const projectJobs = (jobs ?? []).filter((job): job is RenderJob => Boolean(job) && job!.projectId === state.activeProject?.id);
    state.renderJob = currentRenderJob(projectJobs);
    state.renderInFlight = isActiveRender(state.renderJob);
  } catch (err) {
    state.message = `Failed to load the editing workspace: ${String(err)}`;
  }
}

export async function createNewProject(state: EditingViewState, name: string): Promise<void> {
  try {
    const project = await EditingService.CreateDraftProject(name);
    state.projects = [...state.projects, project];
    state.activeProject = project;
    state.selectedClipID = null;
    state.renderJob = null;
    state.message = "Created an empty edit project.";
  } catch (err) { state.message = `Failed to create project: ${String(err)}`; }
}

export function selectClip(state: EditingViewState, clipID: string): void {
  if (!videoClips(state.activeProject).some((clip) => clip.id === clipID)) return;
  state.selectedClipID = clipID;
}

export async function deleteSelectedClip(state: EditingViewState): Promise<void> {
  await mutateSelectedClip(state, "delete");
}

export async function moveSelectedClipEarlier(state: EditingViewState): Promise<void> {
  await mutateSelectedClip(state, "earlier");
}

export async function moveSelectedClipLater(state: EditingViewState): Promise<void> {
  await mutateSelectedClip(state, "later");
}

async function mutateSelectedClip(state: EditingViewState, operation: "delete" | "earlier" | "later"): Promise<void> {
  const project = state.activeProject;
  const clipID = state.selectedClipID;
  if (!project || !clipID) return;
  try {
    const updated = operation === "delete"
      ? await EditingService.DeleteClip(project.id, clipID)
      : operation === "earlier"
        ? await EditingService.MoveClipEarlier(project.id, clipID)
        : await EditingService.MoveClipLater(project.id, clipID);
    state.activeProject = updated;
    state.projects = state.projects.map((item) => item.id === updated.id ? updated : item);
    const remaining = videoClips(updated);
    state.selectedClipID = remaining.some((clip) => clip.id === clipID) ? clipID : remaining.at(-1)?.id ?? null;
    state.message = operation === "delete" ? "Clip removed and project saved." : "Clip order updated and project saved.";
  } catch (err) {
    state.message = `Could not update the clip: ${String(err)}`;
  }
}

export async function saveProject(state: EditingViewState): Promise<void> {
  if (!state.activeProject) return;
  try {
    const saved = await EditingService.SaveProject(state.activeProject);
    state.projects = state.projects.map((project) => project.id === saved.id ? saved : project);
    state.activeProject = saved;
    state.message = "Project saved.";
  } catch (err) { state.message = `Failed to save project: ${String(err)}`; }
}

export async function startRender(state: EditingViewState): Promise<void> {
  if (!state.activeProject || !state.capabilities?.renderReady || videoClips(state.activeProject).length === 0) return;
  state.renderInFlight = true;
  state.message = "Submitting render job.";
  try {
    const job = await EditingService.Render(state.activeProject.id);
    state.renderJob = job ?? null;
    state.renderInFlight = isActiveRender(job ?? null);
    state.message = job?.status === "completed" ? "Render uploaded to OneDrive." : job?.message || "Render submitted.";
  } catch (err) {
    state.renderInFlight = false;
    state.message = ["cancellation_requested", "canceled"].includes(state.renderJob?.status ?? "") ? "Render canceled." : `Render error: ${String(err)}`;
  }
}

export async function cancelRender(state: EditingViewState): Promise<void> {
  if (!state.renderJob || !isActiveRender(state.renderJob)) return;
  state.message = "Requesting render cancellation.";
  try {
    state.renderJob = await EditingService.CancelRender(state.renderJob.id) ?? state.renderJob;
    state.renderInFlight = isActiveRender(state.renderJob);
  }
  catch (err) { state.message = `Cancellation error: ${String(err)}`; }
}

export function setupEditingEvents(state: EditingViewState, reRender: () => void): () => void {
  const updateJob = (data: unknown, completed: boolean) => {
    const job = RenderJob.createFrom(data as Partial<RenderJob>);
    if (job.projectId !== state.activeProject?.id) return;
    state.renderJob = job;
    state.renderInFlight = !completed && isActiveRender(job);
    if (completed) state.message = job.status === "completed" ? "Render uploaded to OneDrive." : job.status === "canceled" ? "Render canceled." : job.errorDetail || job.message || state.message;
    reRender();
  };
  const unsubProgress = Events.On("editing:render:progress", (event) => updateJob(event.data, false));
  const unsubCompleted = Events.On("editing:render:completed", (event) => updateJob(event.data, true));
  return () => { unsubProgress(); unsubCompleted(); };
}

export function renderEditingPanel(state: EditingViewState): string {
  const project = state.activeProject;
  const capabilities = state.capabilities;
  const clips = videoClips(project);
  const eligibleAssets = state.assets.filter((asset) => asset.analysisJobId && asset.analysisStatus === "succeeded");
  const renderable = Boolean(project && clips.length > 0 && capabilities?.renderReady);
  const activeRender = isActiveRender(state.renderJob);
  const preset = project?.renderPreset || capabilities?.supportedRenderPresets[0] || "mpeg4-1080p";

  return `<div class="edit-studio">
    <header class="edit-commandbar">
      <div class="edit-project-identity"><span class="edit-section-mark" aria-hidden="true">E</span><div><strong>${escapeHTML(project?.name || "Editing")}</strong><span>${project ? "Persisted non-destructive project" : "No project selected"}</span></div></div>
      <div class="edit-command-actions">
        <button class="edit-text-button" type="button" data-action="new-project">New project</button>
        ${project ? `<button class="edit-text-button" type="button" data-action="save-project">Save</button>` : ""}
        ${activeRender && capabilities?.renderCancellation ? `<button class="edit-text-button" type="button" data-action="cancel-render">Cancel render</button>` : ""}
        ${renderable ? `<button class="edit-export-button" type="button" data-action="start-render" ${activeRender ? "disabled" : ""}>${activeRender ? "Rendering" : "Render to OneDrive"}</button>` : ""}
      </div>
    </header>
    <div class="edit-workspace">
      <aside class="edit-projects-panel" aria-label="Edit projects">
        <div class="edit-pane-title"><strong>Projects</strong><span>${state.projects.length}</span></div>
        <div class="edit-project-list">${state.projects.length ? state.projects.map((item) => `<button type="button" class="edit-project-row ${item.id === project?.id ? "is-selected" : ""}" data-action="select-project" data-project-id="${escapeHTML(item.id)}" aria-pressed="${item.id === project?.id}"><strong>${escapeHTML(item.name)}</strong><small>${videoClips(item).length} ordered video clip${videoClips(item).length === 1 ? "" : "s"}</small></button>`).join("") : `<div class="edit-empty"><strong>No edit projects</strong><p>Create a project from a Smart Edit suggestion, or start an empty project.</p></div>`}</div>
      </aside>
      <div class="edit-main-panel">
        <section class="edit-overview" aria-labelledby="edit-timeline-title">
          <div><p class="edit-kicker">Ordered video track</p><h2 id="edit-timeline-title">${project ? escapeHTML(project.name) : "Select or create a project"}</h2><p>${project?.originJobId ? `Created from Smart Edit job ${escapeHTML(project.originJobId)}.` : "Only persisted source ranges are shown. Preview and timing preparation are not available yet."}</p></div>
          ${project ? `<div class="edit-provenance"><span>Sources</span><strong>${project.assetIds?.length || 0}</strong><span>Suggestion</span><strong>${escapeHTML(project.suggestionId || "Not recorded")}</strong></div>` : ""}
        </section>
        ${project ? renderOrderedTrack(clips, state.assets, state.selectedClipID, capabilities) : ""}
        ${project && clips.length === 0 ? `<div class="edit-unavailable"><strong>This project has no renderable clips.</strong><p>Addition and trim ranges require media preparation, which has not been implemented. Create a project from a Smart Edit suggestion with grounded time ranges.</p></div>` : ""}
        ${project ? renderRenderPanel(state, preset, renderable) : ""}
      </div>
      <aside class="edit-assets-panel" aria-label="Analyzed source media">
        <div class="edit-pane-title"><strong>Analyzed media</strong><span>${eligibleAssets.length}</span></div>
        <div class="edit-asset-list">${eligibleAssets.length ? eligibleAssets.map((asset) => `<article class="edit-asset-row"><strong>${escapeHTML(asset.name || asset.id)}</strong><dl><div><dt>Analysis</dt><dd>${asset.analysisScenes} scene${asset.analysisScenes === 1 ? "" : "s"}</dd></div><div><dt>Source</dt><dd>${asset.cloudAssetId ? "OneDrive linked" : "OneDrive item unavailable"}</dd></div><div><dt>File</dt><dd>${escapeHTML(asset.contentType || "Type unavailable")} · ${formatBytes(asset.sizeBytes)}</dd></div></dl></article>`).join("") : `<div class="edit-empty"><strong>No analyzed media</strong><p>Analyze imported OneDrive assets before creating a Smart Edit project.</p></div>`}</div>
      </aside>
    </div>
    <div class="edit-live-status" role="status" aria-live="polite">${escapeHTML(state.message || capabilities?.renderRecoveryMessage || "Editing workspace ready.")}</div>
  </div>`;
}

function renderOrderedTrack(clips: ReturnType<typeof videoClips>, assets: ProjectAsset[], selectedClipID: string | null, capabilities: EditingCapabilities | null): string {
  const byID = new Map(assets.map((asset) => [asset.id, asset]));
  return `<section class="edit-track" aria-label="Ordered video clips"><div class="edit-track-head"><strong>Video</strong><span>${clips.length} / ${capabilities?.maximumRenderClips ?? 64} clips · Cuts only</span></div>${clips.length ? `<ol>${clips.map((clip, index) => {
    const asset = byID.get(clip.sourceAssetId);
    const selected = clip.id === selectedClipID;
    const canMove = Boolean(capabilities?.clipReordering);
    const canDelete = Boolean(capabilities?.clipRemoval && clips.length > 1);
    return `<li class="${selected ? "is-selected" : ""}"><button type="button" class="edit-clip-select" data-action="select-clip" data-clip-id="${escapeHTML(clip.id)}" aria-pressed="${selected}"><span class="edit-clip-number">${index + 1}</span><span><strong>${escapeHTML(asset?.name || clip.sourceAssetId)}</strong><small>Source range ${clip.inMs} ms - ${clip.outMs} ms</small></span><span class="edit-clip-cut">${index ? "Cut" : "Start"}</span></button>${selected && (canMove || canDelete) ? `<span class="edit-clip-actions" aria-label="Edit selected clip">${canMove ? `<button type="button" class="edit-clip-action" data-action="move-clip-earlier" ${index === 0 ? "disabled" : ""}>Move earlier</button><button type="button" class="edit-clip-action" data-action="move-clip-later" ${index === clips.length - 1 ? "disabled" : ""}>Move later</button>` : ""}${canDelete ? `<button type="button" class="edit-clip-action is-danger" data-action="delete-clip">Remove</button>` : ""}</span>` : ""}</li>`;
  }).join("")}</ol>` : ""}</section>`;
}

function renderRenderPanel(state: EditingViewState, preset: string, renderable: boolean): string {
  const capabilities = state.capabilities;
  const job = state.renderJob;
  const active = isActiveRender(job);
  const progress = Math.max(0, Math.min(100, job?.percent ?? 0));
  const recovery = capabilities?.renderRecoveryMessage || (capabilities ? "A valid ordered clip sequence is required before rendering." : "Checking render availability.");
  return `<section class="edit-render-panel" aria-labelledby="edit-render-title"><div><p class="edit-kicker">Async export</p><h2 id="edit-render-title">Render status</h2></div>${renderable && capabilities ? `<label class="edit-preset-label">Render preset<select data-action="set-preset" ${active ? "disabled" : ""}>${capabilities.supportedRenderPresets.map((item) => `<option value="${escapeHTML(item)}" ${item === preset ? "selected" : ""}>${escapeHTML(item)}</option>`).join("")}</select></label>` : `<div class="edit-render-blocked"><strong>Rendering unavailable</strong><p>${escapeHTML(recovery)}</p></div>`}${job ? `<div class="edit-job"><div><strong>${escapeHTML(job.status || "submitted")}</strong><span>${escapeHTML(job.message || job.errorDetail || "Waiting for render activity.")}</span></div>${active ? `<progress value="${progress}" max="100">${progress}%</progress><span>${Math.round(progress)}%</span>` : ""}${job.status === "completed" && job.outputDriveItemId ? `<p>Published <strong>${escapeHTML(job.outputName || "render")}</strong> to OneDrive (item ${escapeHTML(job.outputDriveItemId)}).</p>` : ""}${job.status === "failed" ? `<p class="edit-job-error">${escapeHTML(job.errorDetail || "Render failed.")}</p>` : ""}</div>` : ""}</section>`;
}
