package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// FootageRequest is the Collins-facing ask for footage, kept as its own
// entity from Job: a request is what a customer asks for and its lifecycle
// (submitted -> reviewed -> retrieving -> fulfilled/declined), a Job is the
// actual recorder retrieval work an operator starts, possibly against a
// different time range or after clarifying with the requester. LinkJob ties
// the two together once retrieval begins, but a request can exist, and be
// reviewed, entirely without a job.
type FootageRequest struct {
	ID                               int64
	CustomerID, SiteID, RequestedBy  int64
	RequesterName, CamerasJSON       string
	IncidentDate, StartTime, EndTime string
	// SlotsJSON is the full list of date/time ranges this request covers
	// (JSON array of TimeSlot), including the first one already mirrored
	// into IncidentDate/StartTime/EndTime above. Empty on a request
	// submitted before multiple slots existed, or one that only ever had a
	// single slot — Slots() falls back to those three columns either way.
	SlotsJSON                     string
	Reference, Category, Comments string
	Status                        string
	JobID                         sql.NullInt64
	ReviewedBy, ReviewerNotes     string
	CreatedAt, UpdatedAt          time.Time
	SiteName, CustomerName        string
}

// TimeSlot is one date/time range within a footage request. Date and time
// strings are kept as the browser's own <input type=date>/<input
// type=time> values (site-local, no timezone applied) — same convention as
// the legacy IncidentDate/StartTime/EndTime columns.
type TimeSlot struct {
	Date  string `json:"date"`
	Start string `json:"start"`
	End   string `json:"end"`
}

// Slots returns every date/time range this request covers. It prefers
// SlotsJSON, but falls back to a single slot built from
// IncidentDate/StartTime/EndTime for a request that predates multi-slot
// support (or a row where SlotsJSON failed to parse).
func (v FootageRequest) Slots() []TimeSlot {
	if v.SlotsJSON != "" {
		var slots []TimeSlot
		if err := json.Unmarshal([]byte(v.SlotsJSON), &slots); err == nil && len(slots) > 0 {
			return slots
		}
	}
	if v.IncidentDate == "" && v.StartTime == "" && v.EndTime == "" {
		return nil
	}
	return []TimeSlot{{Date: v.IncidentDate, Start: v.StartTime, End: v.EndTime}}
}

const requestStatusSubmitted = "submitted"

const requestColumns = `r.id,r.customer_id,r.site_id,r.requested_by,r.requester_name,r.cameras_json,r.incident_date,r.start_time,r.end_time,r.slots_json,r.reference,r.category,r.comments,r.status,r.job_id,r.reviewed_by,r.reviewer_notes,r.created_at,r.updated_at,s.name,cu.name`

func scanRequest(r scanner, v *FootageRequest) error {
	return r.Scan(&v.ID, &v.CustomerID, &v.SiteID, &v.RequestedBy, &v.RequesterName, &v.CamerasJSON, &v.IncidentDate, &v.StartTime, &v.EndTime, &v.SlotsJSON, &v.Reference, &v.Category, &v.Comments, &v.Status, &v.JobID, &v.ReviewedBy, &v.ReviewerNotes, &v.CreatedAt, &v.UpdatedAt, &v.SiteName, &v.CustomerName)
}

func (s *Store) CreateFootageRequest(ctx context.Context, v FootageRequest) (int64, error) {
	if v.Status == "" {
		v.Status = requestStatusSubmitted
	}
	r, err := s.DB.ExecContext(ctx, `INSERT INTO footage_requests(customer_id,site_id,requested_by,requester_name,cameras_json,incident_date,start_time,end_time,slots_json,reference,category,comments,status) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		v.CustomerID, v.SiteID, v.RequestedBy, v.RequesterName, v.CamerasJSON, v.IncidentDate, v.StartTime, v.EndTime, v.SlotsJSON, v.Reference, v.Category, v.Comments, v.Status)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

func (s *Store) GetFootageRequest(ctx context.Context, id int64) (FootageRequest, error) {
	var v FootageRequest
	err := scanRequest(s.DB.QueryRowContext(ctx, `SELECT `+requestColumns+` FROM footage_requests r JOIN sites s ON s.id=r.site_id JOIN customers cu ON cu.id=r.customer_id WHERE r.id=?`, id), &v)
	return v, err
}

// ListFootageRequests supports three scopes used by the review/portal pages:
// requestedBy>0 alone restricts to one requester's own submissions ("own"
// scope); customerID>0 restricts to one tenant ("sites"/"all" scope for a
// Collins user, or a customer filter on the operational review page);
// neither set lists everything (operational, unscoped).
func (s *Store) ListFootageRequests(ctx context.Context, customerID, requestedBy int64, limit int) ([]FootageRequest, error) {
	q := `SELECT ` + requestColumns + ` FROM footage_requests r JOIN sites s ON s.id=r.site_id JOIN customers cu ON cu.id=r.customer_id WHERE 1=1`
	var args []any
	if customerID > 0 {
		q += ` AND r.customer_id=?`
		args = append(args, customerID)
	}
	if requestedBy > 0 {
		q += ` AND r.requested_by=?`
		args = append(args, requestedBy)
	}
	q += ` ORDER BY r.id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FootageRequest
	for rows.Next() {
		var v FootageRequest
		if err := scanRequest(rows, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ListFootageRequestsForSites restricts to an explicit set of site IDs, used
// for a "sites" request-scope user who can see requests across their granted
// sites but isn't on AllSites.
func (s *Store) ListFootageRequestsForSites(ctx context.Context, siteIDs []int64, limit int) ([]FootageRequest, error) {
	if len(siteIDs) == 0 {
		return nil, nil
	}
	q := `SELECT ` + requestColumns + ` FROM footage_requests r JOIN sites s ON s.id=r.site_id JOIN customers cu ON cu.id=r.customer_id WHERE r.site_id IN (`
	args := make([]any, 0, len(siteIDs)+1)
	for i, id := range siteIDs {
		if i > 0 {
			q += ","
		}
		q += "?"
		args = append(args, id)
	}
	q += `) ORDER BY r.id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FootageRequest
	for rows.Next() {
		var v FootageRequest
		if err := scanRequest(rows, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// UpdateRequestStatus is used both for operational review (accepted/declined
// with reviewer notes) and to mark a request as retrieving/fulfilled once a
// job is linked.
func (s *Store) UpdateRequestStatus(ctx context.Context, id int64, status, reviewedBy, notes string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE footage_requests SET status=?,reviewed_by=?,reviewer_notes=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, status, reviewedBy, notes, id)
	return err
}

// LinkRequestJob associates a footage request with a single retrieval job
// started separately for it (the manual "Link retrieval job" fallback) and
// advances its status, without touching reviewer fields. A thin,
// backward-compatible wrapper over LinkRequestJobs.
func (s *Store) LinkRequestJob(ctx context.Context, id, jobID int64) error {
	return s.LinkRequestJobs(ctx, id, []int64{jobID}, "", "")
}

// LinkRequestJobs associates one or more retrieval jobs with a footage
// request — recorded in the request_jobs join table so a request whose
// cameras span more than one recorder (and so needed more than one queued
// job) keeps every link, not just one — advances its status to
// "retrieving", and, when reviewedBy/notes are non-empty (the
// approve-and-secure flow, as opposed to the bare manual link), records who
// approved it and why. footage_requests.job_id keeps pointing at the first
// job in jobIDs for anything that still reads it directly.
func (s *Store) LinkRequestJobs(ctx context.Context, requestID int64, jobIDs []int64, reviewedBy, notes string) error {
	if len(jobIDs) == 0 {
		return fmt.Errorf("no jobs to link")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, jobID := range jobIDs {
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO request_jobs(request_id,job_id) VALUES(?,?)`, requestID, jobID); err != nil {
			return err
		}
	}
	if reviewedBy != "" || notes != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE footage_requests SET job_id=?,status='retrieving',reviewed_by=?,reviewer_notes=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, jobIDs[0], reviewedBy, notes, requestID); err != nil {
			return err
		}
	} else {
		if _, err = tx.ExecContext(ctx, `UPDATE footage_requests SET job_id=?,status='retrieving',updated_at=CURRENT_TIMESTAMP WHERE id=?`, jobIDs[0], requestID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListJobsForRequest returns every retrieval job linked to a footage
// request — typically one, but a request whose cameras span more than one
// recorder can have several, all queued together by the approve-and-secure
// action.
func (s *Store) ListJobsForRequest(ctx context.Context, requestID int64) ([]Job, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+jobColumns+` FROM request_jobs rj JOIN jobs j ON j.id=rj.job_id JOIN nvrs n ON n.id=j.nvr_id JOIN sites s ON s.id=n.site_id WHERE rj.request_id=? ORDER BY j.id`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		if err := scanJob(rows, &j); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// ListCompletedJobsForRequester returns every completed retrieval job linked
// (via request_jobs) to a footage request that this user personally
// submitted — the Downloads area's view for anyone without CanDownloadAll,
// scoped to their own requests regardless of their broader RequestScope
// setting (which only governs which requests they can *see*, not whose
// footage they can *download*).
func (s *Store) ListCompletedJobsForRequester(ctx context.Context, userID int64, limit int) ([]Job, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT DISTINCT `+jobColumns+` FROM request_jobs rj JOIN footage_requests fr ON fr.id=rj.request_id JOIN jobs j ON j.id=rj.job_id JOIN nvrs n ON n.id=j.nvr_id JOIN sites s ON s.id=n.site_id WHERE fr.requested_by=? AND j.status='completed' ORDER BY j.id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		if err := scanJob(rows, &j); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// JobLinkedToRequester is the IDOR check behind a single-file/zip download
// for anyone without CanDownloadAll: true only if this job is linked to a
// footage request that userID themselves submitted.
func (s *Store) JobLinkedToRequester(ctx context.Context, jobID, userID int64) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM request_jobs rj JOIN footage_requests fr ON fr.id=rj.request_id WHERE rj.job_id=? AND fr.requested_by=?`, jobID, userID).Scan(&n)
	return n > 0, err
}
