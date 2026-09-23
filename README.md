# HikFootageServer

[![Test and publish container](https://github.com/RickDB/HikFootageServer/actions/workflows/container.yml/badge.svg)](https://github.com/RickDB/HikFootageServer/actions/workflows/container.yml)

A runnable Go MVP for searching and securing historic footage from Hikvision NVRs without iVMS-4200 or the recorder web interface. It uses the NVR's HTTP/HTTPS ISAPI service, stores customer sites and recorder metadata in SQLite, and runs exports in a durable background queue.

## MVP capabilities

- Editable customer site and multi-NVR database with soft deletion
- Add/edit recorder connections and test them from the UI
- HTTP Digest authentication
- Mandatory application login with bcrypt password hashing and database-backed sessions
- Automatic model, serial number, firmware, device type, capability, and camera discovery
- Multi-camera, date/time recording search through `POST /ISAPI/ContentMgmt/search`
- Multi-site retrieval wizard with separate date/time, cameras, and export path per site
- Site bandwidth limits (one simultaneous download by default) plus a global multi-job queue
- Durable SQLite jobs that are safely re-queued after an application restart
- Live site/camera progress, retained success/failure/storage log, and camera/job/site retries
- Direct-download fallbacks for Hikvision firmware differences
- Optional lossless FFmpeg remux to MP4; recorder-native `.dav` is retained by default
- SHA-256 evidence manifest and live JSON status/progress endpoint
- Server-rendered responsive web UI with configurable themes (red/white default), structured logging, Docker image, and Compose setup
- Multi-tenant customer/site/recorder model with role-based access control: an operational role with full access, and a request-only role (e.g. a customer such as Collins) restricted to submitting and reviewing footage requests for their own permitted sites
- A Collins-style customer-facing Footage Request portal, kept as its own workflow from the operational Retrieval Job pipeline, with request review, status tracking, and an append-only audit trail
- User management UI (create/edit accounts, assign role, customer, and site access; reset passwords; disable accounts)
- Manufacturer-agnostic recorder integration layer (`internal/recorder`); Hikvision is the only implementation today, but adding another recorder family means one new adapter package, not a rewrite
- Background 28-day recording-availability check per recorder, separate from the online/offline poller and from a customer's secured-footage retention setting
- Per-customer portal branding (display name, logo text, accent colors) and a configurable secured-footage retention default (informational; nothing is auto-purged)

This is an operational MVP, not a complete evidence-management platform. It deliberately does not yet include one-click "start retrieval from a footage request" (an operator currently links a request to a job number started from the export wizard), S3, pagination beyond 200 matches per camera, or cryptographic signing. Keep it behind HTTPS and a protected network boundary for production use.

Repository: <https://github.com/RickDB/HikFootageServer>  
Container: `ghcr.io/rickdb/hikfootageserver`

## Quick start from GHCR

```bash
cp .env.example .env
# Edit APP_ADMIN_PASSWORD and APP_ENCRYPTION_KEY before starting.
docker compose pull
docker compose up -d
```

Open <http://localhost:8080> and sign in with the administrator configured through `APP_ADMIN_USERNAME` and `APP_ADMIN_PASSWORD`. The bootstrap account is forced to change its password before any recorder or footage page can be opened. Compose pulls `ghcr.io/rickdb/hikfootageserver:latest`, persists SQLite and sessions in a named volume, and writes evidence beneath `./exports`.

To store footage at a full host path, set `HIKFOOTAGE_EXPORT_PATH=/mnt/evidence` in `.env`; Compose mounts it as `/app/exports`. Site paths in the UI are paths as seen by the container, so use `/app/exports/customer-name` for an explicit absolute path. Mount any other absolute destination with a Compose override before selecting it.

Pin a release in production by setting `HIKFOOTAGE_IMAGE_TAG=0.1.0` in `.env`. GHCR packages are private when first published; either make the package public in its GitHub package settings or authenticate the deployment host with `docker login ghcr.io` before pulling.

For a local source build instead of GHCR:

```bash
docker compose -f docker-compose.yml -f docker-compose.build.yml up --build
```

## GitHub and GHCR publishing

The workflow at `.github/workflows/container.yml` runs Go tests and vetting, then builds Linux AMD64 and ARM64 images with an SBOM and provenance. Pull requests are build-tested but never published. A push to the default `main` or `master` branch publishes these tags:

- `latest`
- the branch name
- `sha-<short commit>`

A Git tag such as `v0.1.0` also publishes `0.1.0` and `0.1`. Publishing first uses the repository-provided `GITHUB_TOKEN`. The workflow includes `packages: write`, but repository or organization policy can still deny package creation. If the action reports `permission_denied: write_package`, create a classic personal access token owned by `RickDB` with `write:packages` and `read:packages`, add it as the repository Actions secret `GHCR_TOKEN`, and re-run the workflow. The workflow automatically prefers that secret when present.

In GitHub, also open **Settings → Actions → General → Workflow permissions** and select **Read and write permissions**. If a package with the same name already exists, open that package's settings, connect `RickDB/HikFootageServer`, and grant the repository Actions access. No package appears in GHCR until the first push succeeds.

First push:

```bash
cd /mnt/server/apps/projects/hikfootageserver
git init
git branch -M main
git remote add origin https://github.com/RickDB/HikFootageServer.git
git add .
git commit -m "Initial HikFootageServer release"
git push -u origin main
```

Create a versioned container release:

```bash
git tag -a v0.1.0 -m "HikFootageServer v0.1.0"
git push origin v0.1.0
```

If the GHCR package name already existed before this workflow first published it, connect that package to this repository and grant the repository Actions access in the package settings.

## Run from source

Requirements: Go 1.25+ and optionally FFmpeg.

```bash
cp .env.example .env
set -a; . ./.env; set +a
go run ./cmd/server
```

Configuration is entirely environment-based:

| Variable | Default | Purpose |
|---|---:|---|
| `APP_LISTEN_ADDR` | `:8080` | HTTP listen address |
| `APP_DATABASE_PATH` | `./data/hikvision.db` | SQLite database path |
| `APP_EXPORT_ROOT` | `./exports` | Root for evidence output |
| `APP_ADMIN_USERNAME` | `admin` | Bootstrap administrator username |
| `APP_ADMIN_PASSWORD` | `change-me-now` | Bootstrap password; minimum 12 characters and forced to change after first login |
| `APP_SESSION_TTL` | `12h` | Login session lifetime |
| `APP_SECURE_COOKIES` | `false` | Set `true` when the browser uses HTTPS |
| `APP_ENCRYPTION_KEY` | empty | Encrypts recorder passwords with AES-GCM; strongly recommended |
| `APP_REQUEST_TIMEOUT` | `30s` | Discovery/search request timeout |
| `APP_MAX_CONCURRENT_EXPORTS` | `4` | Global background worker count; each site's limit is enforced separately |
| `APP_FFMPEG_PATH` | `ffmpeg` | FFmpeg executable, or empty to disable remux |
| `APP_FFPROBE_PATH` | derived from `APP_FFMPEG_PATH` | ffprobe executable used to validate a remuxed `.mp4` before trusting it; empty falls back to a file-size sanity check |
| `APP_PRESERVE_RAW` | `true` | Retain the downloaded recorder-native file once its `.mp4` remux is verified playable |
| `APP_ALLOW_CUSTOM_DESTINATIONS` | `true` | Permit absolute server-side destinations; paths must be mounted and writable in Docker |
| `APP_LOG_LEVEL` | `info` | `info` or `debug` |

A production-style example is in `config/sample.env`. If the encryption key is changed or lost, previously stored NVR passwords cannot be recovered.

## Login security

All application pages, mutation routes, job status APIs, recorder credentials, searches, and exports require an authenticated session. Only `/login`, static login assets, and `/healthz` are public. Unauthenticated browser requests are redirected to the login page and unauthenticated API requests receive HTTP `401`.

Administrator passwords are stored as bcrypt hashes. Session identifiers are generated from cryptographically secure random bytes, stored in SQLite only as SHA-256 hashes, expire after `APP_SESSION_TTL`, and are sent using `HttpOnly` and `SameSite=Strict` cookies. Enable `APP_SECURE_COOKIES=true` whenever the public application URL uses HTTPS.

The Compose defaults are `admin` / `change-me-now` for initial setup. Change `APP_ADMIN_PASSWORD` in `.env` before the first deployment. The environment password creates the administrator only when that username does not exist; it does not reset an administrator password on later container restarts.

To verify that the running container is the authenticated build, run `curl -i http://SERVER:8080/`. It must return `303 See Other` with `Location: /login...`. `curl -i http://SERVER:8080/healthz` must include `X-HikFootage-Authentication: required` and `ok auth=required`. If the dashboard returns `200` without a session, the host is still running an older image: publish successfully, then run `docker compose pull && docker compose up -d --force-recreate`.

## Hikvision connection setup

Use a dedicated, least-privilege NVR account that is permitted to search and play back recordings. Enable ISAPI if the recorder exposes a separate enable/disable option. Prefer HTTPS with certificate verification. For a known self-signed recorder certificate, certificate verification can be disabled per NVR; use a trusted network/VPN and migrate to a valid internal certificate where possible.

Do not expose recorder management ports directly to the public Internet. Run this application where it can reach the recorders over a site-to-site VPN or a protected management network, and expose only the application's authenticated HTTPS reverse proxy.

### Ports 80/443 versus 8000

ISAPI is served by the recorder's web service:

- HTTP normally uses port **80**.
- HTTPS normally uses port **443**.
- Both search and a supported direct footage download can stay on that HTTP/HTTPS connection.

Port **8000** is Hikvision's SDK/server port used by HCNetSDK/iVMS-style clients. This MVP does not use it. It is only needed when a particular recorder/firmware does not expose the required recorded-file transfer over ISAPI and the implementation must fall back to Hikvision's native SDK. The UI reports that condition rather than silently opening port 8000 or recording RTSP at playback speed.

## Compatibility and probing assumptions

Hikvision XML and endpoint behavior varies across model families, OEM variants, and firmware branches. The adapter therefore uses observed capabilities rather than a firmware-version table:

1. `GET /ISAPI/System/deviceInfo` detects device identity.
2. `GET /ISAPI/ContentMgmt/InputProxy/channels` is preferred for NVR camera discovery.
3. `GET /ISAPI/Streaming/channels` is the camera-discovery fallback.
4. `GET /ISAPI/ContentMgmt/capabilities` is inspected when available.
5. `POST /ISAPI/ContentMgmt/search` proves recording-search support on the first real search.
6. Export first posts a `downloadRequest` to `/ISAPI/ContentMgmt/download`.
7. It then tries the `playbackURI` query form and, if the returned playback URI itself is HTTP(S), a direct authenticated GET.

The returned `playbackURI` is treated as opaque. The UI clearly labels direct-download support as assumed until a real export succeeds. A firmware that only returns an RTSP URI and rejects all ContentMgmt download forms will fail with an explicit compatibility error. Add a model-specific strategy in `internal/hikvision` after capturing that recorder's sanitized request/response behavior.

Channel discovery converts an InputProxy camera ID such as `3` to its main-stream recording track ID `301`. The Streaming fallback filters obvious sub-stream IDs ending in `02`. Validate these mappings on each target product family.

### Recorder time handling

Retrieval time fields are recorder-local wall-clock values. If an operator selects `14:00`, the search asks the NVR for `14:00`; the browser and server do not convert it to UTC. The Hikvision recorder remains authoritative for its configured timezone and daylight-saving rules. This avoids the one- or two-hour shift produced when a browser-local `datetime-local` value is converted to UTC before an ISAPI search.

## Export layout and integrity

Relative site destinations are constrained beneath `APP_EXPORT_ROOT`. The secure-footage wizard always uses the site's configured export path and automatically creates a portable evidence folder from the case reference and operator-entered recorder time range. Every downloaded `.dav` is remuxed to a `.mp4.tmp` file with FFmpeg; the result is verified with ffprobe (a decodable video stream with a positive duration) and only then renamed into its final `.mp4` path, so a half-written or invalid remux can never appear at the destination filename. A remux that fails, or "succeeds" but produces an unplayable file, is discarded — the original `.dav` is kept instead, and an `*.ffmpeg-diagnostics.txt` file capturing the reason and FFmpeg's output is written alongside it so a reported "missing export" can be diagnosed without reproducing the failure:

```text
exports/<site export path>/<case> - <start time> to <end time>/
  Front Entrance_20260827T201000.dav
  Front Entrance_20260827T201000.mp4
  manifest-job-<id>.json
```

Case references, ranges, camera names, and timecodes are sanitized for both Windows and Linux filename rules. FFmpeg uses stream copy (`-c copy`), so it does not transcode footage. If remux fails, the job preserves the recorder-native file and logs the reason. Each job manifest records the source playback URI, requested segment times, byte length, and SHA-256 of every delivered output.

## HTTP endpoints

- `GET/POST /login` — public authentication entry point
- `POST /logout` — revoke the current session
- `GET /account` — authenticated account and forced password-change screen
- `POST /account/password` — update the signed-in administrator password and revoke other sessions
- `GET /healthz` — minimal unauthenticated container health probe; returns no application data
- `GET /` — sites, recorders, and recent jobs
- `GET /sites/new` — site setup
- `GET/POST /sites/{id}` — edit site settings and per-site bandwidth limit
- `POST /sites/{id}/delete` — soft-delete a site while retaining audit records
- `GET /sites/{id}/retrievals` — live and historical site retrieval detail
- `GET /nvrs/{id}` — recorder configuration and diagnostics
- `POST /nvrs/{id}/test` — discovery/capability probe
- `GET /exports/new` — single-site export wizard (paginated/filterable site picker)
- `POST /api/search` — ISAPI recording search
- `POST /exports/batch` — create one or more durable queued jobs
- `GET /jobs/{id}` — live job screen
- `GET /api/jobs/{id}` — JSON job/camera status, progress, errors, and output paths
- `POST /jobs/{id}/retry` — retry an entire job
- `POST /jobs/{id}/retry/{channel}` — retry one camera
- `POST /jobs/{id}/delete`, `POST /jobs/clear`, `POST /sites/{id}/retrievals/clear` — remove one retrieval, or clear all non-active retrieval history (optionally scoped to a site)
- `GET /api/retrieval-status` — live site summary for the dashboard
- `GET /api/timezones` — canonical IANA timezone list backing the timezone picker
- `GET /api/existing-recorders` — host/port of every configured recorder, used client-side to flag duplicates during import
- `GET /settings`, `POST /settings/theme` — site-wide appearance settings
- `GET /settings/customers`, `POST /settings/customers/{id}` — per-customer portal branding and secured-retention default (operational only)
- `GET /settings/import/uptime-kuma`, `POST /settings/import/uptime-kuma/create` — one-time Uptime Kuma monitor import wizard (Settings menu)
- `GET /users`, `GET /users/new`, `POST /users`, `GET/POST /users/{id}` — user management: role, customer, site access, password reset, disable (operational only)
- `GET /audit` — append-only audit trail viewer (operational only)
- `GET /requests` — footage request list, scoped by role: an operator sees everything, a request-only user sees only what their account is scoped to
- `GET /requests/new`, `POST /requests` — submit a footage request (site and camera picker restricted to the requester's granted sites)
- `GET /requests/{id}` — request detail; a request-only user can only open their own visible requests (IDOR-checked server-side, not just hidden in navigation)
- `POST /requests/{id}/status` — review a request: accepted/declined/retrieving/fulfilled plus reviewer notes (operational only)
- `POST /requests/{id}/link-job` — record which retrieval job (started separately via the export wizard) fulfills a request (operational only)
- `GET /api/sites/{id}/cameras` — access-checked camera lookup used by the request form's camera picker
- `GET /api/cameras/{id}/snapshot` — cached camera preview image (JPEG); access-checked per site, reachable by every role that can already see the camera

Every route above marked "operational only" is enforced server-side (an `opOnly` middleware wrapper), not just hidden from a request-only user's navigation.

## Camera preview thumbnails

Both camera pickers — the **Secure footage** wizard and the footage request form — show a small live preview thumbnail next to each camera, fetched via the recorder's ISAPI snapshot endpoint (`GET /ISAPI/Streaming/channels/{channelID}/picture`) rather than requiring anyone to guess which camera a channel number refers to. Clicking a thumbnail opens a larger preview in a modal popup.

Thumbnails for a site load in parallel (one request per camera, all in flight at once) rather than one at a time, and each image is cached server-side for 4 hours (`internal/snapshot`, a small TTL + singleflight cache — concurrent requests for the same camera while a fetch is already in flight share the one in-flight result instead of hitting the recorder twice) before the next view triggers a refetch. The snapshot endpoint also sets `Cache-Control: private, max-age=14400` so a browser revisit within that window skips the round trip entirely. The endpoint uses the same per-site access check as the camera list itself, so a request-only user only ever sees previews for cameras their account can already reach.

## Development

```bash
go test ./...
go build ./cmd/server
```

The project is intentionally modular:

```text
cmd/server          process startup and shutdown
internal/config     environment configuration
internal/store      SQLite schema and repository (sites, recorders, jobs, customers, users/RBAC, footage requests, audit log)
internal/hikvision  Digest-auth ISAPI adapter and fallbacks
internal/recorder   manufacturer-agnostic recorder Client interface; dispatches to internal/hikvision today
internal/jobs       durable export workers, remux, hash, manifest
internal/monitor    background per-site recorder online/offline poller and 28-day recording-availability check
internal/timezones  canonical IANA timezone list for the timezone picker
internal/web        routes, embedded templates, and assets
```

## Multi-tenancy, roles, and the footage request workflow

Every site belongs to a customer (`internal/store` `customers` table). "Internal Operations" (seeded by default) owns pre-existing/operational data; other customers — Collins is seeded as the first one — are request-only tenants with their own portal branding and secured-retention default (**Settings → Customers & branding**).

Two roles exist today: **operational** (full access: sites, recorders, exports, settings, user management) and **request-only** (e.g. Collins staff: submit and track footage requests only, for sites explicitly granted to their account). A request-only user's `request_scope` controls what they can see beyond their own submissions — just their own, every request for their granted sites, or every request for their whole customer. Create and manage accounts under **Users** (operational only); a new account gets a temporary password and must change it on first sign-in, same as the bootstrap administrator.

A **footage request** (submitted through the Collins-style portal) is a deliberately separate entity from a **retrieval job** (the actual recorder download, started through the operational export wizard): a request captures what a customer is asking for and its review status, a job is the queued technical work. An operator reviews a request (**accepted/declined/retrieving/fulfilled**, with notes), starts the retrieval separately via **Secure footage**, and links the resulting job number back to the request from its detail page. Every request submission, status change, and job link is written to the append-only **audit trail** (**Audit trail**, operational only), which also records every "Recent retrievals" deletion before it happens — clearing history never erases that a retrieval occurred.

## Recorder status polling

Each site has its own recorder status poll interval (default 10 minutes, minimum 5), set on the site form. A background poller pings every recorder's `/ISAPI/System/deviceInfo` on that cadence and updates its online/offline status without touching previously discovered model/serial/firmware data — only the "Test & discover" button does a full rediscovery.

## Recording availability

Independent of the online/offline poller, a background check (every 6 hours, per online recorder) searches the first enabled camera over the last 35 days and records how many days of playable footage the recorder still holds. This is shown as a badge next to each recorder (OK ≥28 days, Warning ≥14, Error <14, Unknown until first checked, Offline when the recorder itself is unreachable). It is distinct from a customer's secured-footage retention setting: availability is what the recorder still has; retention is informational guidance on how long a completed retrieval job's own output should be kept (shown on the job detail page; nothing is auto-purged).

## Uptime Kuma import

**Settings → Import from Uptime Kuma** walks through a one-time bulk import: enter shared defaults (customer name, timezone, recorder protocol/port/credentials, export path root), choose an Uptime Kuma backup export (`Settings → Backup → Export` in Uptime Kuma; parsed entirely in the browser, nothing is uploaded until the final step), select which monitors to import, review the computed site/recorder fields (rows with a missing IPv4 address or a host/port that already exists are flagged and skipped automatically), then create. Each monitor's Friendly Name becomes the site name; its hostname (or, failing that, its URL) is scanned for an IPv4 address to use as the recorder host.
