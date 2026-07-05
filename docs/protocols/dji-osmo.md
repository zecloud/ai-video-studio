# DJI Osmo Action 4 protocol audit

This document records how AI Video Studio will use public DJI/Osmo references while keeping the camera integration replaceable and safe. The Osmo Action 4 media protocol is treated as unconfirmed until tested on real hardware.

## Reference policy

| Reference | License status | Intended use |
| --- | --- | --- |
| [`datagutt/node-osmo`](https://github.com/datagutt/node-osmo) | MIT | Primary Action 3/4/5 BLE behavior reference. Port protocol ideas to Go with attribution; do not embed Node by default. |
| [`xaionaro-go/djictl`](https://github.com/xaionaro-go/djictl) | CC0 | Go reference for DJI BLE, Wi-Fi, DUML, and UDP 9004 diagnostics. Safe to study and adapt, but keep app code behind internal interfaces. |
| [`SemiConscious/osmo-download`](https://github.com/SemiConscious/osmo-download) | No license found during planning | Inspiration only for observed protocol behavior. Do not copy code, structure, constants, or comments until licensing is clarified. |

No code has been copied from `osmo-download`. Any future implementation must be written from observed behavior, local hardware tests, and licensed references.

## Integration boundaries

- `internal/camera` owns media discovery, endpoint probing, `HEAD`, `GET`, byte `Range` reads, and bounded stream requests.
- `internal/dji` owns BLE scan/pairing, Wi-Fi/AP setup, DUML/UDP diagnostics, and camera status.
- Wails-exposed services in `internal/backend` remain thin facades. Real protocol code should live behind interfaces so it can be replaced if DJI behavior differs by firmware or OS BLE stack.
- Original videos must be streamed in bounded chunks to OneDrive upload sessions. The camera importer must not persist a complete original video locally.

## Expected Action 4 validation checklist

Use this checklist before treating any protocol behavior as stable:

1. **BLE scan**
   - Confirm Action 4 advertisements are visible on Windows, macOS, and Linux BLE stacks targeted by Wails.
   - Record device name, model identifier/manufacturer data, RSSI, and reconnect behavior.
2. **BLE pairing**
   - Validate the pairing flow and PIN assumptions from licensed references.
   - Capture failure states: unavailable adapter, denied pairing, timeout, stale pairing, unsupported model, and reconnect after sleep.
3. **Wi-Fi/AP setup**
   - Confirm how the camera AP is enabled or joined from BLE/DUML.
   - Record SSID format, authentication behavior, DHCP timing, gateway IP, and whether the host must switch networks manually.
4. **IP and port assumptions**
   - Start with `192.168.2.1`.
   - Validate default HTTP port `80`, reported/observed media port variants such as `7001`, and UDP diagnostics on `9004`.
   - Treat every port as firmware-dependent until verified.
5. **HTTP `/v2` endpoint**
   - Probe `http://192.168.2.1/v2?storage={0|1}&path=<path>` and the same path on validated port variants.
   - Confirm storage ID mapping for SD/internal media and real Action 4 file paths.
6. **`HEAD` support**
   - Verify status codes, `Content-Length`, `Content-Type`, `Accept-Ranges`, redirects, and auth/session requirements.
   - Define behavior for unknown sizes, missing headers, and 404/403/5xx responses.
7. **`Range` reads**
   - Verify `Range: bytes=start-end` returns `206 Partial Content` and correct `Content-Range`.
   - Test first byte, final byte, mid-file chunks, invalid ranges, reconnect, and retry after Wi-Fi loss.
8. **Media discovery/enumeration**
   - Prefer confirmed listing APIs if available.
   - Validate DUML/UDP file listing if reproducible.
   - Use filename/path enumeration only as a last-resort diagnostic strategy, never as the only production path.
   - Record thumbnails, proxies, duration metadata, date metadata, and whether files are readable while recording.
9. **Error states**
   - Surface camera disconnected, BLE unavailable, pairing failed, AP not joined, endpoint unreachable, range unsupported, media changed, file locked, battery/storage warnings, network timeout, and retry exhaustion.

## Implementation preparation

- Keep `CameraService` methods safe by default: status, discovery, checklist, and media listing remain non-hardware stubs; endpoint probing is explicit, bounded by a short timeout, and reports diagnostics without implying transfer readiness.
- Add a real media connector only after hardware validation proves endpoint, storage, and range behavior.
- Keep `DJIControlService` methods safe by default: scan, pair, Wi-Fi setup, status, and diagnostics must not claim success unless the adapter or command has been implemented and validated.
- Prefer small adapters:
  - BLE adapter informed by `node-osmo` Action 4 behavior and `djictl` Go concepts.
  - Wi-Fi/DUML diagnostics adapter informed by `djictl`.
  - HTTP media adapter written fresh from validated Action 4 observations.
- Log diagnostics as protocol facts, not optimistic success defaults.

## BLE/DUML adapter boundary

The Go boundary now lives in `internal/dji`. It models the protocol facts that are safe to expose before choosing a platform BLE library:

- Expected DJI BLE service and characteristics:
  - service `fff0`
  - write/control characteristic `fff3`
  - pairing notification characteristic `fff4`
  - status/notification characteristic `fff5`
- Default pairing PIN candidate from licensed Action 3/4/5 references: `love`.
- Expected camera network defaults after AP setup or manual Wi-Fi join:
  - gateway/media host `192.168.2.1`
  - HTTP media port candidates `80` and `7001`
  - UDP diagnostics candidate `9004`

`DJIControlService` is wired to a `dji.Controller` interface. On Windows, the default controller now uses `tinygo.org/x/bluetooth` through `WindowsBLEAdapter` for real BLE adapter status, scan, GATT connect, service discovery, notification setup, and a bounded pairing attempt using DJI message framing. On non-Windows builds, `NewDefaultController` still returns `NoopController`.

`NoopController` remains available for tests and unsupported platforms: it returns status, protocol profile, and a diagnostics plan, but returns `ErrAdapterNotConfigured` for BLE scan, pairing, and Wi-Fi setup. This avoids success-shaped fallbacks while keeping the Wails API stable.

### Windows BLE adapter status

Implemented:

- Enable the Windows Bluetooth LE stack through `tinygo.org/x/bluetooth`.
- Treat known benign Windows Runtime initialization HRESULTs from `RoInitialize` as non-fatal (`S_FALSE`, changed COM apartment mode, and observed localized `ERROR_INVALID_FUNCTION`/"Fonction incorrecte"). Actual BLE scan/connect failures are still returned explicitly.
- Scan advertisements for a bounded duration and return address, local name, RSSI, service UUIDs, manufacturer data, and an Osmo/DJI candidate model hint when the name or `fff0` service matches.
- Connect to a selected BLE address.
- Discover DJI GATT service `fff0`.
- Discover write/control `fff3`, pairing notify `fff4`, and status notify `fff5`.
- Enable notifications on `fff4` and `fff5`.
- Briefly wait for an initial `fff4` pairing notification before writing, matching the licensed `node-osmo` Action 3/4/5 sequence when firmware emits it. If no initial `fff4` arrives, continue by writing the pair message anyway and record that fact in the result. Direct reads from `fff4`/`fff5` are intentionally avoided on Windows because the TinyGo/WinRT stack can panic when a readable value is empty.
- Write a framed DJI pair message to `fff3` instead of the raw PIN:
  - target `0x0702`
  - transaction ID `0x8092`
  - type `0x450740`
  - payload prefix plus packed PIN candidate, defaulting to `love`
  - DJI CRC8 header and CRC16 body checksums
- Parse post-write `fff4`/`fff5` responses as DJI messages and report target, transaction ID, type, and payload hex.
- Report `Paired=false` when the pair response or expected `0x8092` payload acknowledgement is missing instead of pretending the camera paired successfully.

Still not implemented/validated:

- Confirmed acknowledgement semantics for the Action 4 firmware; the implemented pair frame is informed by `node-osmo` but still needs hardware confirmation.
- BLE command for camera Wi-Fi/AP setup.
- UDP 9004 DUML diagnostics and file-list commands.
- Cross-platform BLE adapters for macOS/Linux.

Hardware test procedure for Windows:

1. Turn on Windows Bluetooth and keep the Osmo Action 4 close to the PC.
2. Put the camera in wireless/app control mode and keep the screen awake.
3. Start the Wails app.
4. In **BLE / DUML diagnostics**, click **Scan BLE**.
5. Select the Osmo/DJI candidate by name, RSSI, or advertised service UUID.
6. Click **Pair / GATT test**.
7. Treat success as confirmed only when the result reports a parsed DJI response with transaction `0x8092` and payload `0001`. If the result says no initial `fff4` was observed but a pair message was still written, capture whether any later `fff4`/`fff5` response arrived.

Future adapter responsibilities:

1. Validate the Windows `BLEAdapter` against real Action 4 firmware: permissions, scan cancellation, reconnect, stale pairing, and notification contents.
2. Add macOS/Linux BLE adapters or a platform abstraction that preserves the same `Controller` contract.
3. Implement confirmed pairing/write behavior against the DJI GATT profile using fresh Go code informed by `node-osmo` and `djictl`.
4. Implement `WiFiConfigurator` so AP setup can be requested or diagnosed without leaking SSID/password-like data to logs.
5. Implement `DUMLTransport` for UDP 9004 experiments only after the camera is on the AP network and packet behavior is captured on synthetic test media.
6. Keep media transfer in `internal/camera`; BLE/DUML should enable or diagnose connectivity, not own full video streaming.

## Wi-Fi media connector prototype

The Go prototype lives under `internal/camera` and is intentionally limited to unit-testable HTTP media behavior. It does not scan BLE, pair, join Wi-Fi, list real camera files, or persist video data locally.

Implemented assumptions to validate:

- Default camera host: `192.168.2.1`.
- Default HTTP port: `80`.
- Alternate candidate port: `7001`, kept as firmware-dependent until real Action 4 validation confirms it.
- Media endpoint shape: `GET|HEAD /v2?storage={0|1}&path=<normalized path>`.
- Storage mapping used by the prototype: `0` = internal, `1` = SD card.
- Media paths are normalized to rooted camera paths such as `/DCIM/100MEDIA/DJI_0001.MP4`; query strings, fragments, null bytes, and path traversal are rejected.
- Ranged reads use `Range: bytes=<start>-<end>` for bounded chunks, or `bytes=<start>-` for open-ended diagnostics.
- Response metadata parsing records `Content-Length`, `Content-Type`, `Accept-Ranges`, `Content-Range`, `ETag`, and `Last-Modified`.
- The Wails backend now exposes a candidate probe that tests a known media path against the selected port or the default `80`/`7001` candidates using `HEAD` and a one-byte `Range` request before marking the endpoint transferable.

### Hardware validation procedure

Use a real Osmo Action 4 only after confirming the test environment has no private network or credential leakage in logs.

1. **Prepare the camera**
   - Charge the camera and insert a test SD card with non-private clips.
   - Update firmware only if the test matrix records the version before and after.
   - Disable recording before transfer tests unless specifically validating locked/recording behavior.
2. **Scan and pair**
   - Record whether the host OS sees the camera over BLE and whether DJI's normal pairing flow is required before Wi-Fi media access.
   - Capture adapter unavailable, permission denied, stale pairing, and timeout states.
3. **Join camera Wi-Fi**
   - Enable the camera AP through the confirmed DJI flow or manual camera UI.
   - Join the host to the camera Wi-Fi and record SSID, DHCP address, gateway, DNS behavior, and time-to-ready.
   - Confirm `ping 192.168.2.1` only as a connectivity hint; HTTP behavior is the source of truth.
4. **Probe likely ports**
   - Start with `http://192.168.2.1:80`.
   - If port 80 fails, test observed/documented variants such as `7001`, but keep them marked firmware-dependent until repeated.
5. **Probe the endpoint**
   - Use a known test file path from the camera, for example `/DCIM/100MEDIA/<clip>.MP4`.
   - Send `HEAD http://192.168.2.1/v2?storage=1&path=/DCIM/100MEDIA/<clip>.MP4`.
   - Record status, redirects, `Content-Length`, `Content-Type`, `Accept-Ranges`, `ETag`, `Last-Modified`, and errors.
6. **Validate byte ranges**
   - Send `GET` with `Range: bytes=0-0`, a mid-file range, and the final byte.
   - Expect `206 Partial Content` and a matching `Content-Range`.
   - Treat `200 OK` to a range request as "range unsupported" unless the behavior is explained by firmware.
   - Test invalid ranges and Wi-Fi disconnect/reconnect without writing a complete original locally.
7. **Discover listing strategy**
   - Prefer a confirmed listing API or DUML/UDP file enumeration if validated.
   - If no listing API is confirmed, use manual known-path probes only as diagnostics.
   - Do not ship filename brute force as the production discovery path.
8. **Record facts**
   - Store firmware version, OS, BLE adapter, Wi-Fi adapter, storage ID, exact path, endpoint URL, headers, status codes, and retry behavior.
   - Keep logs free of personal media names unless they are synthetic test clips.

## Open questions for hardware validation

- Which BLE library is reliable enough for Wails on Windows/macOS/Linux?
- Does Action 4 expose a stable file listing endpoint, or is DUML/UDP required?
- Is the media endpoint consistently port `80`, `7001`, or firmware-dependent?
- Do active recordings, low battery, or locked screen states change media readability?
- Does the camera support long sequential ranged reads without AP sleep, keepalive, or periodic control messages?
