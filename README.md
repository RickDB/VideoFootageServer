# HikFootageServer

[![Test and publish container](https://github.com/RickDB/HikFootageServer/actions/workflows/container.yml/badge.svg)](https://github.com/RickDB/HikFootageServer/actions/workflows/container.yml)

A runnable Go MVP for searching and securing historic footage from Hikvision NVRs without iVMS-4200 or the recorder web interface. It uses the NVR's HTTP/HTTPS ISAPI service, stores customer sites and recorder metadata in SQLite, and runs exports in a durable background queue.

## MVP capabilities

- Customer site and multi-NVR database
- Add/edit recorder connections and test them from the UI
- HTTP Digest authentication
- Mandatory application login with bcrypt password hashing and database-backed sessions
- Automatic model, serial number, firmware, device type, capability, and camera discovery
- Multi-camera, date/time recording search through `POST /ISAPI/ContentMgmt/search`
- Select individual search results and create a case-oriented export job
- Durable SQLite jobs that are safely re-queued after an application restart
- Direct-download fallbacks for Hikvision firmware differences
- Optional lossless FFmpeg remux to MP4; recorder-native `.dav` is retained by default
- SHA-256 evidence manifest and live JSON status/progress endpoint
- Server-rendered responsive web UI, structured logging, Docker image, and Compose setup

This is an operational MVP, not a complete evidence-management platform. It deliberately does not yet include per-operator account administration, audit identities, retention automation, S3, pagination beyond 200 matches per camera, or cryptographic signing. Keep it behind HTTPS and a protected network boundary for production use.

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

A Git tag such as `v0.1.0` also publishes `0.1.0` and `0.1`. Publishing uses the repository-provided `GITHUB_TOKEN`; no GHCR personal access token secret is needed. The workflow requires the standard `packages: write` repository permission included in the workflow file.

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
| `APP_MAX_CONCURRENT_EXPORTS` | `2` | Background export worker count |
| `APP_FFMPEG_PATH` | `ffmpeg` | FFmpeg executable, or empty to disable remux |
| `APP_PRESERVE_RAW` | `true` | Retain the downloaded recorder-native file |
| `APP_ALLOW_CUSTOM_DESTINATIONS` | `false` | Permit absolute server-side destinations |
| `APP_LOG_LEVEL` | `info` | `info` or `debug` |

A production-style example is in `config/sample.env`. If the encryption key is changed or lost, previously stored NVR passwords cannot be recovered.

## Login security

All application pages, mutation routes, job status APIs, recorder credentials, searches, and exports require an authenticated session. Only `/login`, static login assets, and `/healthz` are public. Unauthenticated browser requests are redirected to the login page and unauthenticated API requests receive HTTP `401`.

Administrator passwords are stored as bcrypt hashes. Session identifiers are generated from cryptographically secure random bytes, stored in SQLite only as SHA-256 hashes, expire after `APP_SESSION_TTL`, and are sent using `HttpOnly` and `SameSite=Strict` cookies. Enable `APP_SECURE_COOKIES=true` whenever the public application URL uses HTTPS.

The Compose defaults are `admin` / `change-me-now` for initial setup. Change `APP_ADMIN_PASSWORD` in `.env` before the first deployment. The environment password creates the administrator only when that username does not exist; it does not reset an administrator password on later container restarts.

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

## Export layout and integrity

Relative destinations are constrained beneath `APP_EXPORT_ROOT`. Each job receives a new non-overwriting directory:

```text
exports/<destination>/<case>-job-<id>/
  01_101_20260827T201000Z.dav
  01_101_20260827T201000Z.mp4
  manifest.json
```

FFmpeg uses stream copy (`-c copy`), so it does not transcode footage. If remux fails, the job preserves the recorder-native file and logs the reason. `manifest.json` records the source playback URI, requested segment times, byte length, and SHA-256 of each delivered output.

## HTTP endpoints

- `GET/POST /login` — public authentication entry point
- `POST /logout` — revoke the current session
- `GET /account` — authenticated account and forced password-change screen
- `POST /account/password` — update the signed-in administrator password and revoke other sessions
- `GET /healthz` — minimal unauthenticated container health probe; returns no application data
- `GET /` — sites, recorders, and recent jobs
- `GET /sites/new` — site setup
- `GET /nvrs/{id}` — recorder configuration and diagnostics
- `POST /nvrs/{id}/test` — discovery/capability probe
- `GET /exports/new` — point-to-point export wizard
- `POST /api/search` — ISAPI recording search
- `POST /exports` — durable job creation
- `GET /jobs/{id}` — live job screen
- `GET /api/jobs/{id}` — JSON status, progress, errors, and output paths

## Development

```bash
go test ./...
go build ./cmd/server
```

The project is intentionally modular:

```text
cmd/server          process startup and shutdown
internal/config     environment configuration
internal/store      SQLite schema and repository
internal/hikvision  Digest-auth ISAPI adapter and fallbacks
internal/jobs       durable export workers, remux, hash, manifest
internal/web        routes, embedded templates, and assets
```
