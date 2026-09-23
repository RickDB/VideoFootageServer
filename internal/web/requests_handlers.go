package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/RickDB/HikFootageServer/internal/recorder"
	"github.com/RickDB/HikFootageServer/internal/safepath"
	"github.com/RickDB/HikFootageServer/internal/store"
)

// visibleRequests implements the review/portal scoping rules: an admin or
// reviewer sees everything, a Collins user sees only what their
// request_scope and site access allow. Kept as one place so the list page
// and the detail page's access check can never drift apart.
func (a *App) visibleRequests(r *http.Request, u store.User) ([]store.FootageRequest, error) {
	if u.CanReviewRequests() {
		return a.store.ListFootageRequests(r.Context(), 0, 0, 300)
	}
	switch u.RequestScope {
	case "all":
		return a.store.ListFootageRequests(r.Context(), u.CustomerID, 0, 300)
	case "sites":
		sites, err := a.store.ListSitesForUser(r.Context(), u)
		if err != nil {
			return nil, err
		}
		ids := make([]int64, len(sites))
		for i, s := range sites {
			ids[i] = s.ID
		}
		return a.store.ListFootageRequestsForSites(r.Context(), ids, 300)
	default: // "own"
		return a.store.ListFootageRequests(r.Context(), 0, u.ID, 300)
	}
}

// canViewRequest is the IDOR check for a single request: the requester can
// always see their own; beyond that it follows the same scoping as the list.
func (a *App) canViewRequest(r *http.Request, u store.User, req store.FootageRequest) (bool, error) {
	if u.CanReviewRequests() || req.RequestedBy == u.ID {
		return true, nil
	}
	if u.CustomerID != req.CustomerID {
		return false, nil
	}
	switch u.RequestScope {
	case "all":
		return true, nil
	case "sites":
		return a.store.UserCanAccessSite(r.Context(), u, req.SiteID)
	default:
		return false, nil
	}
}

func (a *App) requestsList(w http.ResponseWriter, r *http.Request) {
	u, _ := currentUser(r)
	requests, err := a.visibleRequests(r, u)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.render(w, r, "requests_list", pageData{Title: "Footage requests", Requests: requests, Notice: r.URL.Query().Get("notice")})
}

func (a *App) requestForm(w http.ResponseWriter, r *http.Request) {
	u, _ := currentUser(r)
	sites, err := a.store.ListSitesForUser(r.Context(), u)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.render(w, r, "request_form", pageData{Title: "Request footage", Sites: sites})
}

// siteCameras is a small access-checked lookup the request form uses to
// populate its camera picker once a site is chosen, without exposing the
// full site/NVR management surface to a request-only user.
func (a *App) siteCameras(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r)
	if e != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	u, _ := currentUser(r)
	if ok, err := a.store.UserCanAccessSite(r.Context(), u, id); err != nil || !ok {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "no access to this site"})
		return
	}
	nvrs, err := a.store.ListNVRsBySite(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	type cam struct {
		ID        int64  `json:"id"`
		ChannelID string `json:"channel_id"`
		Name      string `json:"name"`
	}
	var out []cam
	for _, n := range nvrs {
		cams, _ := a.store.ListCameras(r.Context(), n.ID)
		for _, c := range cams {
			if c.Enabled {
				out = append(out, cam{ID: c.ID, ChannelID: c.ChannelID, Name: c.Name})
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type requestCamera struct {
	// ID is the cameras.id primary key, added alongside channel_id/name so
	// approveAndSecureHandler can resolve each camera back to the exact NVR
	// it belongs to (a channel_id like "101" is only unique within one
	// recorder, not across every recorder at a site). A request submitted
	// before this field existed has ID 0 for every camera and falls back to
	// the manual "Link retrieval job" flow instead.
	ID        int64  `json:"id"`
	ChannelID string `json:"channel_id"`
	Name      string `json:"name"`
}

func (a *App) createRequest(w http.ResponseWriter, r *http.Request) {
	u, _ := currentUser(r)
	_ = r.ParseForm()
	siteID, _ := strconv.ParseInt(r.FormValue("site_id"), 10, 64)
	if ok, err := a.store.UserCanAccessSite(r.Context(), u, siteID); err != nil || !ok {
		http.Error(w, "You don't have access to that site", http.StatusForbidden)
		return
	}
	var cams []requestCamera
	if err := json.Unmarshal([]byte(r.FormValue("cameras_json")), &cams); err != nil || len(cams) == 0 {
		sites, _ := a.store.ListSitesForUser(r.Context(), u)
		a.render(w, r, "request_form", pageData{Title: "Request footage", Sites: sites, Error: "Choose at least one camera."})
		return
	}
	// One or more incident date/time ranges — the form always submits
	// slots_json (even for a single slot), but tolerate the older three
	// scalar fields alone too, for anything still posting the old shape.
	var slots []store.TimeSlot
	if raw := r.FormValue("slots_json"); raw != "" {
		_ = json.Unmarshal([]byte(raw), &slots)
	}
	var cleaned []store.TimeSlot
	for _, sl := range slots {
		sl.Date, sl.Start, sl.End = strings.TrimSpace(sl.Date), strings.TrimSpace(sl.Start), strings.TrimSpace(sl.End)
		if sl.Date == "" || sl.Start == "" || sl.End == "" {
			continue
		}
		cleaned = append(cleaned, sl)
	}
	if len(cleaned) == 0 {
		if d, s, e := strings.TrimSpace(r.FormValue("incident_date")), strings.TrimSpace(r.FormValue("start_time")), strings.TrimSpace(r.FormValue("end_time")); d != "" && s != "" && e != "" {
			cleaned = []store.TimeSlot{{Date: d, Start: s, End: e}}
		}
	}
	if len(cleaned) == 0 {
		sites, _ := a.store.ListSitesForUser(r.Context(), u)
		a.render(w, r, "request_form", pageData{Title: "Request footage", Sites: sites, Error: "At least one incident date, start time and end time is required."})
		return
	}
	slotsJSON, err := json.Marshal(cleaned)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	req := store.FootageRequest{
		CustomerID:    u.CustomerID,
		SiteID:        siteID,
		RequestedBy:   u.ID,
		RequesterName: u.Username,
		CamerasJSON:   r.FormValue("cameras_json"),
		IncidentDate:  cleaned[0].Date,
		StartTime:     cleaned[0].Start,
		EndTime:       cleaned[0].End,
		SlotsJSON:     string(slotsJSON),
		Reference:     strings.TrimSpace(r.FormValue("reference")),
		Category:      strings.TrimSpace(r.FormValue("category")),
		Comments:      strings.TrimSpace(r.FormValue("comments")),
	}
	id, err := a.store.CreateFootageRequest(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = a.store.LogAudit(r.Context(), u.Username, "request.submitted", "footage_request", id, fmt.Sprintf("site_id=%d cameras=%d", siteID, len(cams)))
	http.Redirect(w, r, fmt.Sprintf("/requests/%d?notice=%s", id, url.QueryEscape("Request submitted.")), http.StatusSeeOther)
}

func (a *App) requestDetail(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	req, e := a.store.GetFootageRequest(r.Context(), id)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	u, _ := currentUser(r)
	if ok, err := a.canViewRequest(r, u, req); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if !ok {
		http.NotFound(w, r)
		return
	}
	var cams []requestCamera
	_ = json.Unmarshal([]byte(req.CamerasJSON), &cams)
	names := make([]string, 0, len(cams))
	for _, c := range cams {
		names = append(names, c.Name)
	}
	jobs, err := a.store.ListJobsForRequest(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.render(w, r, "request_detail", pageData{Title: fmt.Sprintf("Request #%d", req.ID), Request: req, RequestCameras: names, RequestSlots: req.Slots(), RequestJobs: jobs, Notice: r.URL.Query().Get("notice")})
}

var allowedRequestStatuses = map[string]bool{"submitted": true, "accepted": true, "declined": true, "retrieving": true, "fulfilled": true}

func (a *App) updateRequestStatusHandler(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	_ = r.ParseForm()
	status := r.FormValue("status")
	if !allowedRequestStatuses[status] {
		http.Error(w, "Unknown status", http.StatusBadRequest)
		return
	}
	actor, _ := currentUser(r)
	if err := a.store.UpdateRequestStatus(r.Context(), id, status, actor.Username, strings.TrimSpace(r.FormValue("reviewer_notes"))); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = a.store.LogAudit(r.Context(), actor.Username, "request.status_changed", "footage_request", id, "status="+status)
	http.Redirect(w, r, fmt.Sprintf("/requests/%d?notice=%s", id, url.QueryEscape("Status updated.")), http.StatusSeeOther)
}

// linkRequestJobHandler records that a retrieval job (started separately
// through the operational export wizard) is fulfilling this request. Kept
// as a manual fallback alongside approveAndSecureHandler's one-click flow —
// e.g. for a legacy request whose cameras can't be auto-resolved, or when
// an operator deliberately wants to search a different time range than what
// was originally requested.
func (a *App) linkRequestJobHandler(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	_ = r.ParseForm()
	jobID, err := strconv.ParseInt(r.FormValue("job_id"), 10, 64)
	if err != nil || jobID < 1 {
		http.Error(w, "A valid job number is required", http.StatusBadRequest)
		return
	}
	if _, err := a.store.GetJob(r.Context(), jobID); err != nil {
		http.Error(w, "That job number doesn't exist", http.StatusBadRequest)
		return
	}
	if err := a.store.LinkRequestJob(r.Context(), id, jobID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	actor, _ := currentUser(r)
	_ = a.store.LogAudit(r.Context(), actor.Username, "request.linked_job", "footage_request", id, fmt.Sprintf("job_id=%d", jobID))
	http.Redirect(w, r, fmt.Sprintf("/requests/%d?notice=%s", id, url.QueryEscape("Linked to retrieval job.")), http.StatusSeeOther)
}

// requestNVRGroup accumulates the channel IDs a footage request touches on
// one recorder, so approveAndSecureHandler can run one recorder search per
// NVR instead of one per camera.
type requestNVRGroup struct {
	nvr        store.NVR
	channelIDs []string
}

// slotWindow parses a request's TimeSlot into recorder-local start/end
// instants, rolling the end forward a day when it isn't after the start
// (only makes sense if the incident crossed midnight, e.g. 23:00 to 01:00).
func slotWindow(sl store.TimeSlot) (start, end time.Time, err error) {
	start, e1 := parseRecorderWallTime(sl.Date + "T" + sl.Start)
	end, e2 := parseRecorderWallTime(sl.Date + "T" + sl.End)
	if e1 != nil || e2 != nil {
		return start, end, fmt.Errorf("couldn't parse %s %s–%s", sl.Date, sl.Start, sl.End)
	}
	if !end.After(start) {
		end = end.Add(24 * time.Hour)
	}
	return start, end, nil
}

// approveAndSecureHandler is the one-click "approve and directly secure
// footage" action: it re-runs the same recorder search the manual Secure
// footage wizard would, once per (recorder, incident time range) pair —
// grouped per recorder since a request's cameras can span more than one NVR
// at a site, and repeated per slot since a request can cover more than one
// date/time range — queues a retrieval job for whatever footage each search
// finds, links every job it created to the request, and advances its status
// to "retrieving" — all from information the request form already
// collected, without an operator re-entering the site, time range(s), or
// cameras a second time in the export wizard.
func (a *App) approveAndSecureHandler(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	req, e := a.store.GetFootageRequest(r.Context(), id)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	if req.Status == "declined" {
		http.Redirect(w, r, fmt.Sprintf("/requests/%d?notice=%s", id, url.QueryEscape("This request was declined; change its status before securing footage.")), http.StatusSeeOther)
		return
	}
	actor, _ := currentUser(r)

	var cams []requestCamera
	if err := json.Unmarshal([]byte(req.CamerasJSON), &cams); err != nil || len(cams) == 0 {
		http.Redirect(w, r, fmt.Sprintf("/requests/%d?notice=%s", id, url.QueryEscape("This request has no cameras to secure footage for.")), http.StatusSeeOther)
		return
	}

	// Group the request's cameras by the recorder each one actually belongs
	// to (resolved via the stored camera ID, not the channel_id alone, since
	// a channel_id such as "101" is only unique within a single recorder).
	var order []int64
	groups := map[int64]*requestNVRGroup{}
	var unresolved []string
	for _, c := range cams {
		cam, err := a.store.GetCamera(r.Context(), c.ID)
		if c.ID == 0 || err != nil || cam.SiteID != req.SiteID {
			unresolved = append(unresolved, c.Name)
			continue
		}
		g, ok := groups[cam.NVRID]
		if !ok {
			nvr, err := a.store.GetNVR(r.Context(), cam.NVRID)
			if err != nil {
				unresolved = append(unresolved, c.Name)
				continue
			}
			g = &requestNVRGroup{nvr: nvr}
			groups[cam.NVRID] = g
			order = append(order, cam.NVRID)
		}
		g.channelIDs = append(g.channelIDs, cam.ChannelID)
	}
	if len(order) == 0 {
		http.Redirect(w, r, fmt.Sprintf("/requests/%d?notice=%s", id, url.QueryEscape("Couldn't resolve this request's cameras to a recorder — link a retrieval job manually instead.")), http.StatusSeeOther)
		return
	}

	slots := req.Slots()
	if len(slots) == 0 {
		http.Redirect(w, r, fmt.Sprintf("/requests/%d?notice=%s", id, url.QueryEscape("This request's incident date/time couldn't be parsed.")), http.StatusSeeOther)
		return
	}

	site, err := a.store.GetSite(r.Context(), req.SiteID)
	if err != nil {
		http.Error(w, "Site not found", http.StatusInternalServerError)
		return
	}
	caseRef := strings.TrimSpace(req.Reference)
	if caseRef == "" {
		caseRef = fmt.Sprintf("REQ-%d", req.ID)
	}
	description := strings.TrimSpace(req.Category)
	if req.Comments != "" {
		if description != "" {
			description += " — "
		}
		description += req.Comments
	}

	var createdJobs []int64
	var emptyNVRs, failedNVRs []string
	for _, sl := range slots {
		start, end, err := slotWindow(sl)
		if err != nil {
			failedNVRs = append(failedNVRs, err.Error())
			continue
		}
		for _, nvrID := range order {
			g := groups[nvrID]
			label := g.nvr.Name
			if len(slots) > 1 {
				label = fmt.Sprintf("%s (%s %s–%s)", g.nvr.Name, sl.Date, sl.Start, sl.End)
			}
			client, err := recorder.New(g.nvr, a.cfg.RequestTimeout)
			if err != nil {
				failedNVRs = append(failedNVRs, label+": "+err.Error())
				continue
			}
			recs, err := client.Search(r.Context(), g.channelIDs, start, end)
			if err != nil {
				failedNVRs = append(failedNVRs, label+": "+err.Error())
				continue
			}
			if len(recs) == 0 {
				emptyNVRs = append(emptyNVRs, label)
				continue
			}
			siteCams, _ := a.store.ListCameras(r.Context(), g.nvr.ID)
			names := map[string]string{}
			for _, sc := range siteCams {
				names[sc.ChannelID] = sc.Name
			}
			for i := range recs {
				recs[i].Name = names[recs[i].ChannelID]
			}
			recordingsJSON, err := json.Marshal(recs)
			if err != nil {
				failedNVRs = append(failedNVRs, label+": "+err.Error())
				continue
			}
			folder := safepath.Component(fmt.Sprintf("%s - %s to %s", caseRef, start.Format("2006-01-02T15:04"), end.Format("2006-01-02T15:04")), "evidence")
			destination := filepath.Join(strings.TrimSpace(site.Destination), folder)
			jobID, err := a.store.CreateJob(r.Context(), store.Job{NVRID: g.nvr.ID, CaseRef: caseRef, Description: description, Destination: destination, DestinationIsFinal: true, RecordingsJSON: string(recordingsJSON)})
			if err != nil {
				failedNVRs = append(failedNVRs, label+": "+err.Error())
				continue
			}
			createdJobs = append(createdJobs, jobID)
		}
	}

	if len(createdJobs) == 0 {
		detail := "No footage was found on the recorder(s) for this request's cameras and time range(s)."
		if len(failedNVRs) > 0 {
			detail = "Couldn't search the recorder(s): " + strings.Join(failedNVRs, "; ")
		}
		http.Redirect(w, r, fmt.Sprintf("/requests/%d?notice=%s", id, url.QueryEscape(detail)), http.StatusSeeOther)
		return
	}

	autoNote := fmt.Sprintf("Auto-approved and queued %d retrieval job(s) covering %d recorder(s) across %d time range(s).", len(createdJobs), len(order), len(slots))
	if len(emptyNVRs) > 0 {
		autoNote += " No footage found on: " + strings.Join(emptyNVRs, ", ") + "."
	}
	if len(failedNVRs) > 0 {
		autoNote += " Search failed on: " + strings.Join(failedNVRs, "; ") + "."
	}
	if len(unresolved) > 0 {
		autoNote += " Couldn't resolve camera(s): " + strings.Join(unresolved, ", ") + "."
	}
	notes := strings.TrimSpace(req.ReviewerNotes)
	if notes != "" {
		notes += " "
	}
	notes += autoNote

	if err := a.store.LinkRequestJobs(r.Context(), id, createdJobs, actor.Username, notes); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.jobs.Wake()
	_ = a.store.LogAudit(r.Context(), actor.Username, "request.approved_and_queued", "footage_request", id, fmt.Sprintf("jobs=%v", createdJobs))
	http.Redirect(w, r, fmt.Sprintf("/requests/%d?notice=%s", id, url.QueryEscape(fmt.Sprintf("Approved and queued %d retrieval job(s).", len(createdJobs)))), http.StatusSeeOther)
}
