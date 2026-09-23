package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/RickDB/HikFootageServer/internal/store"
)

// visibleRequests implements the review/portal scoping rules: an operator
// sees everything, a Collins user sees only what their request_scope and
// site access allow. Kept as one place so the list page and the detail
// page's access check can never drift apart.
func (a *App) visibleRequests(r *http.Request, u store.User) ([]store.FootageRequest, error) {
	if u.IsOperational() {
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
	if u.IsOperational() || req.RequestedBy == u.ID {
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
		ChannelID string `json:"channel_id"`
		Name      string `json:"name"`
	}
	var out []cam
	for _, n := range nvrs {
		cams, _ := a.store.ListCameras(r.Context(), n.ID)
		for _, c := range cams {
			if c.Enabled {
				out = append(out, cam{ChannelID: c.ChannelID, Name: c.Name})
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type requestCamera struct {
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
	req := store.FootageRequest{
		CustomerID:    u.CustomerID,
		SiteID:        siteID,
		RequestedBy:   u.ID,
		RequesterName: u.Username,
		CamerasJSON:   r.FormValue("cameras_json"),
		IncidentDate:  strings.TrimSpace(r.FormValue("incident_date")),
		StartTime:     strings.TrimSpace(r.FormValue("start_time")),
		EndTime:       strings.TrimSpace(r.FormValue("end_time")),
		Reference:     strings.TrimSpace(r.FormValue("reference")),
		Category:      strings.TrimSpace(r.FormValue("category")),
		Comments:      strings.TrimSpace(r.FormValue("comments")),
	}
	if req.IncidentDate == "" || req.StartTime == "" || req.EndTime == "" {
		sites, _ := a.store.ListSitesForUser(r.Context(), u)
		a.render(w, r, "request_form", pageData{Title: "Request footage", Sites: sites, Error: "Incident date, start time and end time are required."})
		return
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
	a.render(w, r, "request_detail", pageData{Title: fmt.Sprintf("Request #%d", req.ID), Request: req, RequestCameras: names, Notice: r.URL.Query().Get("notice")})
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
// through the operational export wizard) is fulfilling this request. Full
// one-click "start retrieval from this request" is deferred; this keeps the
// request/job link and audit trail intact in the meantime.
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
