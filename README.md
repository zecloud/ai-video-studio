# AI Video Studio

AI Video Studio is the independent desktop product for transfers, OneDrive 365, Azure Content Understanding, Video Indexer Studio, cloud library, non-destructive editing, and FFmpeg-based renders. It accepts camera-originated media as an available source as well as local media. It does not control or diagnose DJI hardware.

AI Video Camera is a separate independent desktop product for Osmo Action BLE scan/pairing, Wi-Fi/AP setup, DUML/UDP, and HTTP media-endpoint probes and diagnostics only. Camera does not own cloud authentication, transfers, or post-production. There is no inter-app transfer, sync, RPC, socket, or shared-file bridge between the products, and none is planned.

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

The Go services are safe stubs: they do not store credentials, authenticate to Microsoft/Azure, command DJI hardware, or persist complete original videos locally. The no-full-local-original rule remains in force for camera-originated ingestion; bounded streaming/chunking and permitted metadata, thumbnails/proxies, and explicit renders are the only documented exceptions.
