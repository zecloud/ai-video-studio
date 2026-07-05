# AI Video Studio

Desktop scaffold for importing DJI Osmo Action 4 footage, streaming originals to OneDrive 365, analyzing with Azure Content Understanding, and preparing non-destructive AI-assisted edits.

## Prerequisites

- Node.js 20+ and npm for the TypeScript/Vite frontend.
- Go 1.23+ for the backend module.
- Wails v3 CLI (`wails3`) and platform WebView dependencies for desktop dev/build.
- FFmpeg and ffprobe are runtime dependencies for editing/export workflows. The current backend can detect the runtime, probe media, and build thumbnail extraction commands; see `docs/processing/ffmpeg-bindings.md`.

## Commands

```bash
npm install --prefix frontend
npm run check --prefix frontend
npm run build --prefix frontend
go test ./...
task dev
task app:build
wails3 generate bindings -ts
```

`task dev` runs `wails3 dev -config ./build/config.yml` and starts the Vite frontend in Wails dev mode. `task app:build` compiles the native Wails app; the internal `build` task is the Wails frontend asset hook.

The Go services are safe stubs: they do not store credentials, authenticate to Microsoft/Azure, command DJI hardware, or persist complete original videos locally.
