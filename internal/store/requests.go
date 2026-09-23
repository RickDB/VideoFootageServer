package store

import (
	"context"
	"database/sql"
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
	Reference, Category, Comments    string
	Status                           string
	JobID                            sql.NullInt64
	ReviewedBy, ReviewerNotes        string
	CreatedAt, UpdatedAt             time.Time
	SiteName, CustomerName           string
}

const requestStatusSubmitted = "submitted"

const requestColumns = `r.id,r.customer_id,r.site_id,r.requested_by,r.requester_name,r.cameras_json,r.incident_date,r.start_time,r.end_time,r.reference,r.category,r.comments,r.status,r.job_id,r.reviewed_by,r.reviewer_notes,r.created_at,r.updated_at,s.name,cu.name`

func scanRequest(r scanner, v *FootageRequest) error {
	return r.Scan(&v.ID, &v.CustomerID, &v.SiteID, &v.RequestedBy, &v.RequesterName, &v.CamerasJSON, &v.IncidentDate, &v.StartTime, &v.EndTime, &v.Reference, &v.Category, &v.Comments, &v.Status, &v.JobID, &v.ReviewedBy, &v.ReviewerNotes, &v.CreatedAt, &v.UpdatedAt, &v.SiteName, &v.CustomerName)
}

func (s *Store) CreateFootageRequest(ctx context.Context, v FootageRequest) (int64, error) {
	if v.Status == "" {
		v.Status = requestStatusSubmitted
	}
	r, err := s.DB.ExecContext(ctx, `INSERT INTO footage_requests(customer_id,site_id,requested_by,requester_name,cameras_json,incident_date,start_time,end_time,reference,category,comments,status) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		v.CustomerID, v.SiteID, v.RequestedBy, v.RequesterName, v.CamerasJSON, v.IncidentDate, v.StartTime, v.EndTime, v.Reference, v.Category, v.Comments, v.Status)
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

// LinkRequestJob associates a footage request with the retrieval job started
// for it and advances its status, without touching reviewer fields.
func (s *Store) LinkRequestJob(ctx context.Context, id, jobID int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE footage_requests SET job_id=?,status='retrieving',updated_at=CURRENT_TIMESTAMP WHERE id=?`, jobID, id)
	return err
}
