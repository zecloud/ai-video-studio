# Microsoft Entra and Microsoft Graph configuration

AI Video Studio uses Microsoft Graph to stream DJI Osmo Action 4 originals into the user's OneDrive. The desktop app must be registered as a **public client** in Microsoft Entra ID and must use delegated user permissions. Do not create or embed a client secret for the desktop app.

## App registration

Create one Microsoft Entra app registration for the desktop client:

| Setting | Recommended value |
| --- | --- |
| Name | `AI Video Studio Desktop` |
| Supported account types | Single tenant for an organization build; multi-tenant only if the product explicitly supports external Microsoft 365 tenants |
| Platform | Mobile and desktop applications / public client |
| Redirect URI | Loopback redirect for authorization code + PKCE, for example `http://localhost` or an app-specific loopback URI supported by the chosen MSAL library |
| Allow public client flows | Enabled only when the selected SDK/device-code flow requires it |
| Client secret | None |

Prefer the authorization code flow with PKCE for the Wails desktop app because it keeps the user's sign-in in a browser and protects the authorization code without a secret. The current MVP implements device-code auth first because it is reliable in Wails without a loopback listener; the UI shows the verification URL, user code, expiry, and polling state.

## Delegated Microsoft Graph permissions

Start with delegated permissions only:

| Scope | Use in AI Video Studio | Consent notes |
| --- | --- | --- |
| `Files.ReadWrite.AppFolder` | Default. Lets the app read/write files in its own OneDrive app folder for imports and rendered exports. | Least-privilege starting point. Surface this scope in onboarding/settings. |
| `Files.ReadWrite` | Only when the product must let users choose or update files outside the app folder, such as a user-selected project folder, existing library assets, or exports to arbitrary OneDrive paths. | Broader access. Require an explicit product reason and user-facing explanation before requesting it. |

Do not request broad Graph scopes "just in case." If a feature needs a broader scope, document the feature, update the app registration, and handle permission-limited states in the UI.

## Runtime configuration

Store only non-sensitive configuration in app settings or `config.json`:

- tenant ID or tenant alias
- application/client ID
- auth flow preference (`authorization_code_pkce` or `device_code`)
- registered redirect URI
- requested Graph scopes
- OneDrive destination mode and display path
- transfer chunk size and retry preferences

Never store client secrets, refresh tokens, access tokens, SAS URLs, private media paths, or generated caches in a committed config file.

The default local settings use tenant `organizations`, auth flow `device_code`, delegated `Files.ReadWrite.AppFolder`, and add `offline_access` only when requesting tokens so refresh can work without broadening file permissions.

## Token storage expectations

Desktop clients are public clients. They cannot protect a client secret, and any persistent token cache must be treated as sensitive user data.

- The current Windows implementation stores the OneDrive token cache as a DPAPI-protected blob in the user's app config directory (`onedrive-token.dpapi`). The blob is bound to the Windows user profile and is not written to `settings.json`.
- The cache is scoped to the configured tenant ID, client ID, and Graph scopes, so changing the app registration or permission set requires signing in again.
- `Sign out` clears both the in-memory token and the DPAPI cache file.
- Use the selected MSAL/token library's secure cache integration where available.
- Persist tokens only in OS-appropriate secure storage: Windows Credential Manager/DPAPI, macOS Keychain, or Linux Secret Service/libsecret.
- If secure storage is unavailable, fall back to session-only tokens and require the user to sign in again.
- Never log access tokens, refresh tokens, ID tokens, authorization codes, device codes after completion, upload-session URLs, or `Authorization` headers.
- Clear the token cache on sign-out and expose token-expired/consent-required states as recoverable auth errors.

## OneDrive destination model

The default destination is the app folder:

```text
/Apps/AI Video Studio
```

Imports should create Microsoft Graph upload sessions under that folder and stream camera ranges directly into those sessions. The transfer path must not persist a complete original camera video locally.

Use a broader user-selected OneDrive destination only after requesting `Files.ReadWrite` and explaining why app-folder access is no longer sufficient.

## Upload session behavior

The OneDrive foundation builds upload sessions with injectable HTTP and token-provider interfaces so tests do not require real Graph credentials. The default request target is the least-privilege app folder endpoint:

```text
POST /me/drive/special/approot:/<relative-file-path>:/createUploadSession
```

The request body sets `@microsoft.graph.conflictBehavior` explicitly, defaulting to `rename`. Upload chunks are planned sequentially with `Content-Range` headers in the form:

```text
Content-Range: bytes <start>-<end>/<total-size>
```

Chunk sizes must be multiples of Microsoft Graph's 320 KiB alignment requirement except for the final chunk. The default example chunk size remains 10 MiB and imports remain single-transfer by default (`maxConcurrentImports: 1`) until retry and resume behavior is validated with real footage.

Resumable responses are parsed from `nextExpectedRanges`; the lowest returned start offset becomes the next sequential chunk start. Upload session URLs are sensitive bearer-like URLs: never log them, persist them in committed config, or expose them in UI diagnostics.

## Upload validation checklist

- Create sessions using only delegated `Files.ReadWrite.AppFolder` unless a documented feature requires `Files.ReadWrite`.
- Verify the generated session path stays under `/Apps/AI Video Studio` for app-folder mode.
- Confirm every non-final chunk is aligned to 320 KiB and carries an exact `Content-Length`.
- Interrupt a transfer and resume from Graph `nextExpectedRanges` without restarting completed chunks.
- Confirm failed, expired, and malformed upload-session states return actionable errors.
- Confirm no full original video is written to local disk during camera-to-OneDrive streaming.

## References

- Microsoft identity platform: OAuth 2.0 authorization code flow with PKCE for public clients.
- Microsoft identity platform: device code flow.
- Microsoft Graph OneDrive file permissions: `Files.ReadWrite.AppFolder` and `Files.ReadWrite`.
- MSAL token cache guidance for secure persistence on desktop platforms.
