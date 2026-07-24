# AI Video Studio design foundation

## Product ownership boundary

This is the UI specification for **AI Video Studio**, the independent transfer, cloud, analysis, and post-production product. **AI Video Camera** is a separate independent desktop product whose UI is limited to Osmo Action BLE scan/pairing, Wi-Fi/AP setup, DUML/UDP, and HTTP media-endpoint probes and diagnostics. Do not place either product's responsibilities in the other product's navigation, onboarding, empty states, or component labels.

There is no inter-app transfer, sync, RPC, socket, or shared-file bridge—permanent or otherwise. Do not design a handoff button, paired-app status, shared inbox, or shared-file convention.

## Design intent

AI Video Studio is a dense, practical desktop tool. The UI should help users understand source availability, cloud transfer, AI analysis, and editing state at a glance. Favor tables, split panes, queues, timelines, diagnostics, and inline recovery over decorative layouts.

## OKLCH design tokens

Use OKLCH tokens as the source of truth. Convert to fallback formats only at build time if needed.

```css
:root {
  color-scheme: light;

  --color-bg: oklch(98% 0.006 250);
  --color-surface: oklch(100% 0 0);
  --color-surface-subtle: oklch(96% 0.008 250);
  --color-surface-raised: oklch(99% 0.004 250);
  --color-border: oklch(87% 0.012 250);
  --color-border-strong: oklch(73% 0.018 250);

  --color-ink: oklch(21% 0.025 255);
  --color-ink-muted: oklch(46% 0.026 255);
  --color-ink-subtle: oklch(58% 0.022 255);
  --color-ink-inverse: oklch(99% 0.004 250);

  --color-accent: oklch(55% 0.16 255);
  --color-accent-hover: oklch(49% 0.17 255);
  --color-accent-soft: oklch(93% 0.035 255);

  --color-success: oklch(55% 0.13 155);
  --color-success-soft: oklch(94% 0.045 155);
  --color-warning: oklch(72% 0.14 80);
  --color-warning-soft: oklch(95% 0.055 80);
  --color-danger: oklch(57% 0.19 28);
  --color-danger-soft: oklch(94% 0.045 28);
  --color-info: oklch(58% 0.13 230);
  --color-info-soft: oklch(94% 0.04 230);

  --focus-ring: oklch(62% 0.17 255);
  --shadow-raised: 0 1px 2px oklch(21% 0.025 255 / 0.08),
    0 8px 24px oklch(21% 0.025 255 / 0.08);
}

@media (prefers-color-scheme: dark) {
  :root {
    color-scheme: dark;

    --color-bg: oklch(18% 0.018 255);
    --color-surface: oklch(22% 0.02 255);
    --color-surface-subtle: oklch(26% 0.022 255);
    --color-surface-raised: oklch(28% 0.024 255);
    --color-border: oklch(36% 0.024 255);
    --color-border-strong: oklch(48% 0.028 255);

    --color-ink: oklch(94% 0.008 250);
    --color-ink-muted: oklch(76% 0.014 250);
    --color-ink-subtle: oklch(66% 0.016 250);
    --color-ink-inverse: oklch(18% 0.018 255);

    --color-accent: oklch(70% 0.14 255);
    --color-accent-hover: oklch(76% 0.13 255);
    --color-accent-soft: oklch(30% 0.055 255);

    --color-success: oklch(72% 0.12 155);
    --color-success-soft: oklch(30% 0.05 155);
    --color-warning: oklch(80% 0.13 80);
    --color-warning-soft: oklch(32% 0.055 80);
    --color-danger: oklch(70% 0.16 28);
    --color-danger-soft: oklch(31% 0.06 28);
    --color-info: oklch(72% 0.12 230);
    --color-info-soft: oklch(31% 0.055 230);

    --focus-ring: oklch(76% 0.15 255);
    --shadow-raised: 0 1px 2px oklch(0% 0 0 / 0.24),
      0 12px 32px oklch(0% 0 0 / 0.28);
  }
}
```

Semantic usage:

- Accent: primary action, active navigation, selected timeline segment.
- Success: uploaded, analyzed, rendered, connected.
- Warning: degraded network, retrying, service limits, missing optional data.
- Danger: failed transfer, expired auth that blocks work, destructive cancellation confirmation.
- Info: diagnostics, queued operations, analysis pending.

## Typography

- Use a system sans-serif stack: `Inter`, `Segoe UI`, `Roboto`, `Helvetica Neue`, `Arial`, sans-serif.
- Use tabular numbers for sizes, duration, speed, ETA, timecodes, and progress.
- Keep copy direct. Use verbs on buttons and status labels.

| Token | Size | Line height | Use |
| --- | ---: | ---: | --- |
| `text-xs` | 0.75rem | 1rem | Metadata, badges, table secondary text |
| `text-sm` | 0.875rem | 1.25rem | Default UI text, labels, table cells |
| `text-md` | 1rem | 1.5rem | Body copy, form controls |
| `text-lg` | 1.125rem | 1.625rem | Panel titles |
| `text-xl` | 1.25rem | 1.75rem | Screen titles |
| `text-2xl` | 1.5rem | 2rem | Major workflow title only |

Recommended weights: 400 for body, 500 for labels and table headers, 600 for screen and panel headings.

## Spacing and layout

Base spacing scale:

| Token | Value | Use |
| --- | ---: | --- |
| `space-1` | 0.25rem | Tight gaps, icon/text gap |
| `space-2` | 0.5rem | Control internals, compact row gaps |
| `space-3` | 0.75rem | Form groups, table toolbar gaps |
| `space-4` | 1rem | Panel padding |
| `space-6` | 1.5rem | Screen section gaps |
| `space-8` | 2rem | Major layout gaps |

Layout rules:

- Desktop default: left navigation, main workspace, optional right details panel.
- Use split panes for browser/detail, analysis/detail, and editing/properties.
- Keep primary actions in persistent toolbars, not buried in cards.
- Tables should support dense rows, sorting, selection, status badges, and inline actions.
- Timelines need visible time rulers, segment labels, selected state, and keyboard-operable trim handles.
- Long-running queues should remain visible while users inspect other screens.

## Component guidance

### Onboarding

- Use a stepper with persistent progress: Microsoft 365, Azure, storage policy, and FFmpeg check. Do not include camera pairing or camera network setup; those belong to AI Video Camera.
- Explain zero full local original storage before the first transfer.
- Show permission scope, destination folder, and why each integration is required.
- Prefer inline validation rows over modal alerts.
- Provide diagnostics for missing FFmpeg, unavailable WebView dependencies, and failed auth.

### Transfer source and media browser

- Primary layout: media table plus details/preview panel.
- Treat this view as Studio's available transfer inputs, not as a camera-control surface. It may represent camera-originated media or local media, but it must not scan, pair, configure, or diagnose Osmo hardware.
- Columns: selection, thumbnail if available, name, type, duration, size, date, storage, transfer status, diagnostics.
- Support multi-select, select all visible, keyboard range selection, and a persistent selection bar.
- Show source availability, metadata, and transfer readiness. Camera BLE/Wi-Fi/AP, DUML/UDP, endpoint, battery, and reconnect diagnostics are not Studio components.
- Use warning states for unconfirmed size, missing range support, or an unavailable source.

### Transfer queue

- Show global progress, current file progress, bytes uploaded, speed, ETA when reliable, retry count, and current chunk/range.
- States: queued, preparing, uploading, paused, retrying, completed, cancelled, failed.
- Actions: pause if technically supported, cancel, retry, renew auth, and open Studio diagnostics.
- Errors must name the failing system: source, OneDrive Graph, network, local runtime, or service limit. Do not imply Studio can repair camera connectivity.
- Never imply a complete original file was stored locally.

### Project library

- Use a searchable, filterable table of cloud assets and edit projects.
- Show OneDrive status, Azure analysis status, project status, render status, source device, date, duration, and size.
- Provide direct actions: open analysis, create edit, open OneDrive link, retry analysis, view metadata.
- Empty state should explain the import workflow and show the next setup step.

### Analysis studio

- Primary layout: video/preview area, analysis timeline, transcript/highlight list, details panel.
- Scenes, transcript segments, highlight candidates, and suggestions must share timecode references.
- Allow marking, rejecting, and promoting highlights into an edit draft.
- Scores should be secondary signals, not the only explanation for a suggestion.
- Support copying timecodes and exporting structured metadata when implemented.

### Editing studio

- Editing is non-destructive. Always show source asset references and output target separately.
- Timeline supports segment order, in/out trims, delete, reorder, simple transitions, captions/titles, audio level basics, and render preset.
- Use a properties panel for selected segment settings.
- Preview/render states should clearly distinguish generated proxies, temporary preview artifacts, and final exports.
- Render progress includes current operation, percent if available, elapsed time, logs, cancellation, output path, and OneDrive upload state if enabled.

## Common states

Every major component should define:

- Empty: no data yet and clear next action.
- Loading: skeleton or determinate progress when possible.
- Ready: normal actionable state.
- Selected: visible without relying on color alone.
- Disabled: reason available in text or accessible description.
- Warning: degraded but usable.
- Error: failed with cause, affected system, and recovery action.
- Offline/disconnected: transfer source or cloud unavailable.
- Auth expired: renewal action and impact.
- Permission-limited: current scope and what cannot be done.

## Motion

- Use 150-250 ms transitions for panel entry, row expansion, selection feedback, and progress changes.
- Avoid decorative motion, looping animations, and attention-stealing effects.
- Progress indicators may animate only while work is active.
- Under `prefers-reduced-motion: reduce`, remove non-essential transitions and use static state changes.

## Responsive behavior

Target desktop first:

- Compact window: collapse navigation to icons, stack details panel below main content, keep queues accessible.
- Laptop: left navigation + main content; details panel can slide or dock.
- Large display: three-pane layouts are allowed for browser/detail/diagnostics and analysis/detail/transcript.
- Tables should keep critical columns visible and move lower-priority metadata into row details on narrow widths.
- Editing timeline should remain horizontally scrollable with stable toolbar and time ruler.

## Keyboard, focus, and accessibility rules

- All primary workflows must be operable by keyboard.
- Use logical tab order: navigation, toolbar, primary content, details panel, status/queue.
- Focus rings must be visible against light and dark surfaces.
- Use roving tabindex for dense lists, tables with row actions, tab strips, and timeline segment navigation.
- Provide skip links or equivalent shortcuts for moving to main workspace and queue.
- Do not use color alone for status; combine icon, label, and accessible text.
- Announce long-running operation state changes through an appropriate live region.
- Ensure table headers, sort direction, selected rows, progress values, and timeline controls have accessible names.
- Destructive actions require explicit labels and confirmation only when cancellation cannot be recovered.
