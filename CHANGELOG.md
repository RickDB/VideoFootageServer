# Changelog

All notable changes to this project are documented here. Format loosely follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- Monitoring overview replaces GDPR surveys with attention-first site findings, recording/screenshot coverage charts, per-check history graphs, expandable NVR/camera results, and site-summary CSV reports.
- Persistent daily/weekly monitoring schedules with weekday selection, interval, start date/time/timezone, all/selected sites, editing and pause/resume, restart catch-up, overlap prevention, and audit logging.

- Administrator GDPR surveys area: saved export root, all/selected installation screenshot exports with site-named folders and dark-frame skips, per-camera 28-day playback date/time checks, persistent background runs with live progress, error logs, CSV/JSON reports, and audit records.

### Fixed

- GDPR surveys now automatically discover and save cameras for recorders without saved channels, show discovery progress, and continue other recorders when discovery fails. Previously newly imported installations failed immediately with "no enabled cameras". Existing disabled channels remain disabled.

- The click-to-enlarge camera preview's larger modal (0.5.1) still rendered a modest-resolution camera snapshot near its own small native size — `max-width`/`max-height` only cap how big the modal/image are allowed to grow, they don't force a small image to actually get bigger. The modal now claims an explicit box sized to the display (`96vw` × `94vh`) and the image fills it via `object-fit: contain`, scaling a small snapshot up to match (letterboxed, never stretched or distorted).
- The secured-footage preview shown in Downloads and job pages (as opposed to a live recorder snapshot) was extracted with ffmpeg at a hardcoded 240px wide, regardless of the recording's actual resolution — so it looked small even opened directly at native size, and no amount of CSS scaling could add back detail that was thrown away at extraction time. It's now extracted close to the source's own resolution (capped at 1920px wide so an unusually high-res recording doesn't produce an oversized cache file). Existing secured footage gets a freshly extracted frame the next time its preview is viewed, rather than staying stuck with the old small cached one.
- The site retrievals page kept polling every 1.5s for the life of the page, and each poll tore down and re-fetched every job's camera thumbnails from scratch — including already-completed retrievals whose thumbnails never change — which showed up as a continuous blink. Already-loaded thumbnails are now cached client-side across polls so only a camera's own status/progress text updates each tick, not its image; the same fix applies to a still-active job's own camera progress list on the job detail page, where an already-finished camera's thumbnail would blink while its neighbors were still downloading.
- The live camera preview in the Secure footage wizard came back empty for every camera on some recorders (confirmed on a DS-7604NI-K1/4P(B), firmware V4.30.097): the recorder rejected `GET /ISAPI/Streaming/channels/{id}/picture` outright with a generic "Invalid XML Content" error for a channel that is itself an IP camera proxied through the NVR, even though the same channel ID works fine for search and download — the ISAPI spec documents that `picture` sub-resource as optional, and this firmware apparently doesn't implement it for proxied channels the way newer firmware does. The snapshot fetch now retries via the parallel `ContentMgmt/InputProxy` picture endpoint before giving up.
- "Recording availability" on the recorders list only ever searched one representative camera per recorder (whichever came first) and reported that single camera's retention as the whole recorder's — misleading whenever cameras on the same site don't share retention, which is normal: a high-motion camera fills its storage allocation with more, shorter segments and gets overwritten sooner than a quiet one. It now searches every camera on the recorder and reports whichever one still goes back furthest.

## [0.5.1]

### Added

- Footage requests can now cover more than one date/time slot: the request form has a repeatable "+ Add another time range" control, stored additively (`slots_json`, falling back to the original single date/time columns for older rows so nothing needs a data migration), shown on the request detail and list pages, and honored end-to-end by "Approve & secure footage" — one job per (slot, recorder) combination, with slot-aware labeling when a request has more than one.
- Both camera pickers (the request form and the **Secure footage** wizard) are now a shared, paginated grid of larger click-to-select preview tiles instead of a cramped checkbox list, so choosing among up to 32 cameras on a site stays usable.
- A **Secure footage** wizard limited to a single site now selects it automatically instead of requiring a click, and its button is renamed from "Search selected site" to "Find recordings" — it searches the recorder for footage, not for a site, and the old name read oddly once you'd already have to search a site you were also currently securing.
- A job's completed/failed status is now shown as a themed checkmark/cross badge (dashboard, site retrievals, job detail) instead of the word itself, which read as cramped repeated down a column of jobs; other in-progress statuses still show as text.
- The click-to-enlarge camera preview (Downloads and job pages) now opens at close to full screen size instead of a fixed, fairly small modal, scaling with the display instead of a hardcoded pixel cap.

### Fixed

- The job detail page's "Secured footage retention" line ran its two parts together with no line break or spacing (`32 daysInformational — until 25 Oct 2026`) — the informational note is now its own line.
- Button text was invisible against its own background in some themes: a `.actions>a` CSS rule outranked `.primary`/`.secondary` on specificity and silently overrode their white text with the muted link color.
- The dashboard's tab strip (Customer sites / Recorders / Recent retrievals) didn't switch panels on click — the tab buttons and the panels they control weren't actually nested under the same element the tab script scans for panels.
- `HFTable` in single/multi-select mode never registered a selection for a caller that didn't manually tag its rows with an `_id` — which was every caller except one — so, most visibly, choosing a site in the **Secure footage** wizard and clicking through immediately failed with "Select a site." even though the row was clicked. Rows now get an `_id` derived from their own `ID`/`id` field when the caller doesn't supply one.
- The per-item "Download" button on a completed job served every camera under its bare name (e.g. every "Achtertuin" segment as `Achtertuin.dav`), so more than one segment from the same camera collided into indistinguishable downloads. It now serves the file's actual saved name, which already encodes the camera and its timeslot (e.g. `Achtertuin_2026-09-21_10-58-30.mp4`), and drops a legacy recorder-native filename's trailing UTC "Z". The zip download's own de-duplication had a matching bug — checking one key for a repeat but incrementing a different one, so a collision could never actually be caught — fixed to use the same key both times.
- A camera's progress row (job detail page, and the site retrievals page, which was additionally missing its thumbnail image altogether) rendered its name and status text overlapping the mini progress bar next to it: the CSS that stacks the name above the status text targeted `.camera-progress>div:first-child`, which silently stopped matching the moment a thumbnail element was placed in front of it. Replaced with an explicit class so the layout no longer depends on sibling order.

## [0.5.0]

### Added — request-processing and Downloads roles

- Two new roles: **reviewer** (everything needed to process footage requests — review/approve/decline, the one-click approve-and-secure action, the manual Secure footage export wizard, and viewing/retrying the jobs it queues — but no site/recorder/user/settings management) and **downloader** (the Downloads area only). A new `reviewOnly` route tier sits alongside the existing admin-only `opOnly` tier so a reviewer never reaches site/recorder/user/settings management. The default admin account keeps every capability, always — each capability check is an explicit `role == "admin" || …` rather than a separate "is this an admin" check bolted on beside it.
- New **Downloads** area (`/downloads`), reachable by every signed-in role: an admin, reviewer, or downloader sees every completed retrieval across all sites; anyone else (a "collins" request-submitter, or any future minimal role) sees only completed jobs linked to footage requests *they personally submitted* — checked per-download, not just hidden navigation. Supports downloading everything from a job as a zip, or a single camera's file individually.
- Each completed camera on the job page and in Downloads now shows a small click-to-enlarge preview — the first frame of the secured output file itself, extracted once with ffmpeg and cached next to the file, using the same thumbnail/modal UI as the live recorder snapshot preview.
- New endpoints: `GET /downloads`, `GET /downloads/jobs/{id}/zip`, `GET /downloads/jobs/{id}/items/{item}`, `GET /api/job-items/{item}/thumbnail`.

### Changed — rebrand

- Renamed throughout the interface from "HikFootageServer" to **Video Footage Server (VFS)**, including the page title, header mark/logo, login page, and favicon. UI copy that assumed a single manufacturer ("Hikvision remains authoritative…", "Add Hikvision recorder") is now brand-neutral, ahead of non-Hikvision recorder support.
- Footer now reads "© \<year\> Hak Beveiliging · Video Footage Server (VFS) — secure, brand-independent NVR retrieval".

### Fixed

- The "Settings" nav dropdown closed itself before a click could land on it — the menu was positioned with a small gap below the button, and moving the mouse straight down crossed that gap and broke `:hover`. It's now flush against the button with no gap.
- Every page's own inline script (`HF.Table(...)`, camera preview thumbnails, the request form's camera picker, …) ran *before* `app.js` — which defines `window.HF` — had actually executed, because `app.js` was loaded with `defer` while those inline scripts run synchronously during parsing. This silently aborted with `HF is not defined` on every page that used it, which is why tab/table counts could show correctly (server-rendered) while the table content underneath stayed empty (JS never ran). `app.js` is no longer deferred.
- The Content-Security-Policy's `img-src` didn't allow `blob:`, so every snapshot/preview thumbnail — fetched via JS and rendered from an object URL — silently failed to paint even though the underlying fetch succeeded, in every theme.
- A failed-to-load camera preview thumbnail rendered its `alt` text as overflowing, overlapping text instead of a clean placeholder (browsers don't clip a broken image's alt text to its box), which is what made the request form's camera picker look broken/garbled whenever a camera's live snapshot wasn't reachable.
- `HF.Table(...)` crashed with `Cannot read properties of null (reading 'length')` whenever its data was empty — a Go nil slice marshals to JSON `null`, not `[]`, so any zero-row table (no recorders yet, no retrieval history yet, …) took down that whole script block.
- The dashboard's "Retrieval status" widget silently fell back to "No configured sites." once *any* retrieval job existed, even with sites configured — `MAX(j.updated_at)` was scanned into a `time.Time`, but SQLite's own `CURRENT_TIMESTAMP` column default (used whenever a job is created) isn't in the format the driver can convert back, so the whole query failed and its error was silently discarded.
- Button/link text matching its own background in low-contrast combinations, most visibly in the charcoal theme (nav dropdown menu, status/notice/error badges, disabled buttons, the timezone autocomplete list) — these now use the theme's own color tokens instead of hardcoded light-mode colors, and disabled buttons get a proper muted style instead of just fading (which faded the text and background by the same amount, keeping them just as close together).

## [0.4.1]

### Added — one-click "Approve & secure footage"

- A footage request's detail page now has an **Approve & secure footage** action (operational only): it re-runs the recorder search using the request's own site, cameras, and incident date/time — grouped per recorder, since a request's cameras can span more than one NVR at a site — queues a retrieval job for whatever footage is found on each one, links every job it created back to the request, and advances its status to "retrieving". No more re-entering the site/cameras/time range a second time in the Secure footage wizard.
- A request can now be linked to more than one retrieval job (new `request_jobs` join table), for the case where its cameras span multiple recorders. The manual "Link retrieval job" fallback still works as before and is now recorded the same way, so a request's linked-jobs list always reflects every job tied to it regardless of which path created the link.
- The footage request form now stores each chosen camera's database ID alongside its channel ID and name, so the approval flow can resolve exactly which recorder a camera belongs to (a channel ID like "101" is only unique within one recorder, not across every recorder at a site). Requests submitted before this change fall back to the manual link flow, with a clear notice rather than a guess.
- New endpoint: `POST /requests/{id}/approve-and-secure`.

## [0.4.0]

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
