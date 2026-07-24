import "./styles.css";
import * as CameraService from "../bindings/github.com/zecloud/ai-video-studio/internal/cameraapp/cameraservice.js";
import * as DJIControlService from "../bindings/github.com/zecloud/ai-video-studio/internal/cameraapp/djicontrolservice.js";
import { CameraStorage, EndpointProbeRequest } from "../bindings/github.com/zecloud/ai-video-studio/internal/camera/models.js";
import { PairingRequest, WiFiSetupRequest } from "../bindings/github.com/zecloud/ai-video-studio/internal/dji/models.js";

const root = document.querySelector<HTMLDivElement>("#app");
if (!root) throw new Error("App root was not found");
const app = root;
const esc = (value: string): string => value.replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;").replaceAll('"', "&quot;");
let devices: Awaited<ReturnType<typeof DJIControlService.ScanBLE>> = [];
let selected = "";
let message = "Run a diagnostic to verify the local camera boundary.";
let status = "Loading";
let protocol: Awaited<ReturnType<typeof DJIControlService.ProtocolProfile>> | null = null;
let diagnostics: Awaited<ReturnType<typeof DJIControlService.RunDiagnostics>> | null = null;
let endpointIp = "192.168.2.1";
let mediaPath = "/DCIM/100MEDIA";
let wifiProfile: Awaited<ReturnType<typeof DJIControlService.SetupWiFi>> | null = null;
let wifiMessage = "";

function render(): void {
  const ble = protocol?.ble;
  app.innerHTML = `<main class="shell"><header><div><p class="eyebrow">DJI hardware boundary</p><h1>AI Video Camera</h1><p class="lede">Connect, pair, and validate the Osmo Action camera. Transfers and cloud workflows live in AI Video Studio.</p></div><span class="pill">${esc(status)}</span></header>
    <section class="grid"><article class="panel"><div class="panel-head"><div><p class="eyebrow">BLE / DUML profile</p><h2>${esc(protocol?.modelHint || "Osmo Action 4")}</h2></div><button data-action="diagnose">Run diagnostics</button></div>
      <div class="facts"><div><span>GATT service</span><strong>${esc(ble?.serviceUuid || "fff0")}</strong></div><div><span>Write / control</span><strong>${esc(ble?.writeCharUuid || "fff3")}</strong></div><div><span>Notify</span><strong>${esc(ble?.statusCharUuid || "fff5")}</strong></div><div><span>Gateway</span><strong>${esc(protocol?.defaultIp || "192.168.2.1")}</strong></div><div><span>DUML UDP</span><strong>${protocol?.udpPort || 9004}</strong></div><div><span>Media ports</span><strong>${esc(protocol?.mediaPorts?.join(", ") || "80, 7001")}</strong></div></div></article>
      <article class="panel"><div class="panel-head"><div><p class="eyebrow">Windows Bluetooth</p><h2>Nearby devices</h2></div><button data-action="scan">Scan BLE</button></div><div class="devices">${devices.length ? devices.map((device) => `<button class="device ${device.id === selected ? "selected" : ""}" data-device="${esc(device.id)}"><strong>${esc(device.name || "Unnamed peripheral")}</strong><span>${esc(device.model || "BLE peripheral")} · RSSI ${device.rssi}</span><small>${esc(device.address || device.id)}</small></button>`).join("") : `<p class="empty">No scan results. Put the camera in wireless/app control mode, then scan.</p>`}</div><button class="secondary wide" data-action="pair" ${selected ? "" : "disabled"}>Pair / GATT test</button><button class="secondary wide" data-action="wifi" ${selected ? "" : "disabled"}>Set up Wi-Fi / AP</button>${wifiProfile ? `<p class="message" aria-live="polite">${esc(wifiProfile.message || "Wi-Fi profile ready.")} SSID: ${esc(wifiProfile.ssid)} · IP: ${esc(wifiProfile.ipAddress)}</p>` : wifiMessage ? `<p class="message" aria-live="polite">${esc(wifiMessage)}</p>` : ""}</article></section>
    <section class="panel"><div class="panel-head"><div><p class="eyebrow">HTTP media readiness</p><h2>Camera endpoint</h2></div><button class="secondary" data-action="probe">Probe HEAD + Range</button></div><div class="endpoint"><label>Camera IP<input id="ip" value="${esc(endpointIp)}" /></label><label>Media path<input id="path" value="${esc(mediaPath)}" /></label></div><p class="message" aria-live="polite">${esc(message)}</p></section>
    <section class="panel"><div class="panel-head"><div><p class="eyebrow">Validation checklist</p><h2>Hardware readiness</h2></div></div>${diagnostics?.steps?.length ? diagnostics.steps.map((step) => `<div class="step"><span class="pill">${esc(step.status)}</span><div><strong>${esc(step.label)}</strong><p>${esc(step.description)}</p></div><small>${esc(step.transport || "-")}</small></div>`).join("") : `<p class="empty">Run diagnostics to load BLE, Wi-Fi/AP, DUML/UDP, and media endpoint checks.</p>`}</section></main>`;
  app.querySelector<HTMLButtonElement>("[data-action='scan']")?.addEventListener("click", () => void scan());
  app.querySelectorAll<HTMLButtonElement>("[data-device]").forEach((button) => button.addEventListener("click", () => { selected = button.dataset.device || ""; render(); }));
  app.querySelector<HTMLButtonElement>("[data-action='pair']")?.addEventListener("click", () => void pair());
  app.querySelector<HTMLButtonElement>("[data-action='wifi']")?.addEventListener("click", () => void setupWiFi());
  app.querySelector<HTMLButtonElement>("[data-action='diagnose']")?.addEventListener("click", () => void loadDiagnostics());
  app.querySelector<HTMLButtonElement>("[data-action='probe']")?.addEventListener("click", () => void probe());
  app.querySelector<HTMLInputElement>("#ip")?.addEventListener("input", (event) => { endpointIp = (event.target as HTMLInputElement).value; });
  app.querySelector<HTMLInputElement>("#path")?.addEventListener("input", (event) => { mediaPath = (event.target as HTMLInputElement).value; });
}
async function loadDiagnostics(): Promise<void> { message = "Running local hardware diagnostics..."; render(); try { [protocol, diagnostics] = await Promise.all([DJIControlService.ProtocolProfile(), DJIControlService.RunDiagnostics("osmo-action-4")]); status = "Diagnostics loaded"; message = diagnostics.message; } catch (error) { message = error instanceof Error ? error.message : "Diagnostics failed."; status = "Unavailable"; } render(); }
async function scan(): Promise<void> { message = "Scanning BLE advertisements..."; render(); try { devices = await DJIControlService.ScanBLE(); selected = devices[0]?.id || ""; message = `${devices.length} BLE peripheral(s) found.`; } catch (error) { message = error instanceof Error ? error.message : "BLE scan failed."; } render(); }
async function pair(): Promise<void> { if (!selected) return; message = `Testing GATT pairing with ${selected}...`; render(); try { const result = await DJIControlService.Pair(new PairingRequest({ deviceId: selected, pin: protocol?.ble?.defaultPin || "love" })); message = result.message; } catch (error) { message = error instanceof Error ? error.message : "Pairing failed."; } render(); }
async function setupWiFi(): Promise<void> { if (!selected) return; wifiMessage = "Setting up camera Wi-Fi/AP..."; wifiProfile = null; render(); try { wifiProfile = await DJIControlService.SetupWiFi(new WiFiSetupRequest({ deviceId: selected })); wifiMessage = ""; } catch (error) { wifiMessage = error instanceof Error ? error.message : "Wi-Fi setup failed."; } render(); }
async function probe(): Promise<void> { message = "Probing endpoint..."; render(); try { const result = await CameraService.ProbeEndpoint(new EndpointProbeRequest({ ipAddress: endpointIp, path: mediaPath, storage: CameraStorage.CameraStorageSD })); message = result.message; status = !result.reachable ? "Endpoint unavailable" : result.rangeOk ? "Media ready" : "Endpoint reachable"; } catch (error) { message = error instanceof Error ? error.message : "Endpoint probe failed."; status = "Endpoint unavailable"; } render(); }
render();
void (async () => { try { const result = await DJIControlService.Status(); status = result.adapterConfigured ? "Adapter configured" : "Adapter not configured"; protocol = result.protocol; } catch { status = "Unavailable"; } render(); })();
