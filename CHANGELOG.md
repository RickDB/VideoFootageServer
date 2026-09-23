# Changelog

All notable changes to this project are documented here. Format loosely follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added — camera preview thumbnails

- Both camera pickers (**Secure footage** wizard and the footage request form) now show a small live preview thumbnail per camera, fetched from the recorder's Hikvision ISAPI snapshot endpoint; clicking one opens a larger preview in a modal popup instead of crowding the form with full-size images.
- Thumbnails for a site load in parallel rather than one at a time. Images are cached server-side for 4 hours via a small dependency-free TTL + singleflight cache (`internal/snapshot`) — concurrent requests for the same camera during an in-flight fetch share the one result instead of double-hitting the recorder — and the response also sets `Cache-Control: private, max-age=14400` so an in-window browser revisit skips the request entirely.
- New endpoint `GET /api/cameras/{id}/snapshot`, access-checked per site with the same `UserCanAccessSite` rule as the existing camera list endpoint, so it's reachable by request-only users too, not just operational ones.
- `internal/recorder.Client` gained a `Snapshot` method (implemented today by `internal/hikvision.Client.Snapshot`, calling `GET /ISAPI/Streaming/channels/{channelID}/picture`), so a future non-Hikvision adapter picks this up the same way it picks up search/download.

### Added — Collins Footage Request & Retrieval Platform (multi-tenant / RBAC)

- Multi-tenant data model: a new `customers` table (default "Internal Operations" plus a seeded "Collins" tenant), with sites, users and recorders now scoped to a customer. Sites gained location/store number and country fields.
- Role-based access control: an **operational** role (full access, unchanged behavior) and a new **request-only** role for customers such as Collins, restricted to submitting and reviewing footage requests for explicitly granted sites. Enforced server-side by an `opOnly` route middleware and a per-site `UserCanAccessSite` check — never just hidden navigation.
- **Users** admin page: create/edit accounts, assign role/customer/site access, reset passwords (forces a change on next sign-in), disable accounts. A request-only user's `request_scope` controls whether they see only their own submissions, their granted sites', or their whole customer's requests.
- Collins-style **Footage Request portal**: a request-only user can submit a request (site + cameras + incident date/time + reference/category/comments, times shown in the recorder's own local time) and track its status. Kept as a separate entity/table from retrieval jobs so a request can be reviewed and tracked independently of when (or whether) a job is started for it.
- Operational **request review**: accept/decline/mark retrieving/fulfilled with reviewer notes, and link the retrieval job (started separately through the existing export wizard) that fulfills a request.
- Append-only **audit trail** (`GET /audit`, operational only): logs request submissions/status changes/job links, site/recorder/user create and update, and — critically — every job deleted or cleared from "Recent retrievals" is logged *before* removal, so clearing history can never erase that a retrieval happened.
- Manufacturer-agnostic **recorder integration layer** (`internal/recorder`): a `Client` interface and a `New(nvr)` dispatcher keyed on a new `nvrs.manufacturer` column. Hikvision is the only implementation today (unchanged behavior); adding another recorder family is a new adapter package plus one dispatch case, not a rewrite. The recorder form now has a manufacturer picker.
- Background **recording-availability check**: every 6 hours, each online recorder's first enabled camera is searched over the last 35 days to see how many days of playable footage it still holds, shown as an OK/Warning/Error/Unknown/Offline badge on the Recorders table. Distinct from the existing online/offline poller and from the new secured-footage retention setting.
- Per-customer **portal branding**: display name, logo text, and accent colors (`Settings → Customers & branding`), applied to the header and primary color for that customer's portal users; plus a configurable secured-footage retention default (informational, shown on the job detail page — nothing is auto-purged).
- New endpoints: `GET/POST /users`, `GET/POST /users/{id}`, `GET /audit`, `GET /settings/customers`, `POST /settings/customers/{id}`, `GET /requests`, `GET/POST /requests/new` → `/requests`, `GET /requests/{id}`, `POST /requests/{id}/status`, `POST /requests/{id}/link-job`, `GET /api/sites/{id}/cameras`.

### Fixed — DAV→MP4 conversion hardening

- The remux now writes to a `.mp4.tmp` path and only renames it into place after ffprobe confirms it's decodable, so a half-written or invalid file can never appear at the final `.mp4` path even under a crash. A discarded remux now also writes an `*.ffmpeg-diagnostics.txt` file (reason, error, and FFmpeg's own output) next to the preserved `.dav`, so a reported missing/broken export can be diagnosed without reproducing the failure.

### Added — earlier this release

- Timezone picker on the site form: fuzzy/wildcard autocomplete over the full IANA timezone list (`GET /api/timezones`).
- Customer sites and Recorders are now paginated, filterable tables instead of a card grid — built on a shared client-side table component reused across the dashboard, the export wizard's site picker, and the Uptime Kuma import wizard.
- Recorders table: site name is now the first column, with combined filtering by site/recorder name and online/offline/untested status.
- Dashboard reorganized to cut scrolling: a slim top banner, a standalone "Start Retrieval" call-to-action (previously interwoven with the banner), and Customer sites / Recorders / Recent retrievals moved into tabs.
- Export wizard (`/exports/new`): site selection is now a single-select, paginated/filterable table instead of multi-site checkbox cards.
- Per-site recorder status polling: each site has its own poll interval (default 10 minutes, minimum 5) and a background poller pings every recorder on that cadence, independent of the manual "Test & discover" action, without overwriting previously discovered model/serial/capabilities data.
- Retrieval history can now be deleted: per-retrieval delete (dashboard, site retrievals page, job detail page) and "Clear all" (dashboard, or scoped to one site). Active retrievals can't be deleted out from under a running job.
- New one-time import wizard, **Settings → Import from Uptime Kuma**: reads an Uptime Kuma backup export entirely in the browser, lets you choose shared defaults and which monitors to import, reviews the computed site/recorder fields (flagging missing IPv4 addresses and host/port duplicates), then bulk-creates the sites and recorders with per-row results.
- New endpoints: `GET /api/timezones`, `GET /api/existing-recorders`, `POST /jobs/{id}/delete`, `POST /jobs/clear`, `POST /sites/{id}/retrievals/clear`, `GET /settings/import/uptime-kuma`, `POST /settings/import/uptime-kuma/create`.
- `APP_FFPROBE_PATH` config option (auto-derived from `APP_FFMPEG_PATH` when unset).

### Fixed

- `.dav` → `.mp4` remux is now verified with ffprobe (a decodable video stream with a positive duration) before being trusted. Previously, ffmpeg could exit successfully while producing an unplayable/empty `.mp4`, which was then kept (and could even replace the recorder-native `.dav`). A failed or invalid remux is now discarded and the original `.dav` is preserved instead.

## [0.2.0]

_Baseline prior to this changelog._
