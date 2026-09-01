package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func (s *Store) EnsureJobItems(ctx context.Context, job Job) error {
	var count int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_items WHERE job_id=?`, job.ID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	var recordings []Recording
	if err := json.Unmarshal([]byte(job.RecordingsJSON), &recordings); err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, recording := range recordings {
		b, _ := json.Marshal(recording)
		if _, err = tx.ExecContext(ctx, `INSERT INTO job_items(job_id,channel_id,camera_name,recording_json,total_bytes) VALUES(?,?,?,?,?)`, job.ID, recording.ChannelID, recording.Name, string(b), recording.Size); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListJobItems(ctx context.Context, jobID int64) ([]JobItem, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,job_id,channel_id,camera_name,recording_json,status,progress,bytes_downloaded,total_bytes,error,output_path,created_at,updated_at FROM job_items WHERE job_id=? ORDER BY id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []JobItem
	for rows.Next() {
		var item JobItem
		if err = rows.Scan(&item.ID, &item.JobID, &item.ChannelID, &item.CameraName, &item.RecordingJSON, &item.Status, &item.Progress, &item.BytesDownloaded, &item.TotalBytes, &item.Error, &item.OutputPath, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) UpdateJobItem(ctx context.Context, id int64, status string, progress int, downloaded, total int64, errText, outputPath string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE job_items SET status=?,progress=?,bytes_downloaded=?,total_bytes=CASE WHEN ?>0 THEN ? ELSE total_bytes END,error=?,output_path=CASE WHEN ?<>'' THEN ? ELSE output_path END,updated_at=CURRENT_TIMESTAMP WHERE id=?`, status, progress, downloaded, total, total, errText, outputPath, outputPath, id)
	return err
}

func (s *Store) RecalculateJobProgress(ctx context.Context, jobID int64) (int, int, error) {
	var progress, failed int
	err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(CAST(AVG(progress) AS INTEGER),0),COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END),0) FROM job_items WHERE job_id=?`, jobID).Scan(&progress, &failed)
	if err != nil {
		return 0, 0, err
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE jobs SET progress=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, progress, jobID)
	return progress, failed, err
}

func (s *Store) CompletedItemFiles(ctx context.Context, jobID int64) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT output_path FROM job_items WHERE job_id=? AND status='completed' AND output_path<>'' ORDER BY id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []string
	for rows.Next() {
		var path string
		if err = rows.Scan(&path); err != nil {
			return nil, err
		}
		files = append(files, path)
	}
	return files, rows.Err()
}

func (s *Store) ListSiteJobs(ctx context.Context, siteID int64, limit int) ([]Job, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT j.id,j.nvr_id,n.site_id,j.case_ref,j.description,j.destination,j.destination_is_final,j.status,j.progress,j.error,j.recordings_json,j.output_files_json,j.created_at,j.updated_at,n.name,s.name,s.customer FROM jobs j JOIN nvrs n ON n.id=j.nvr_id JOIN sites s ON s.id=n.site_id WHERE n.site_id=? ORDER BY j.id DESC LIMIT ?`, siteID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []Job
	for rows.Next() {
		var job Job
		if err = scanJob(rows, &job); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *Store) RetrievalStatuses(ctx context.Context) ([]SiteRetrievalStatus, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT s.id,s.customer,s.name,COALESCE(SUM(CASE WHEN j.status IN ('starting','downloading','verifying') THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN j.status='queued' THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN j.status='failed' THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN j.status='completed' THEN 1 ELSE 0 END),0),COALESCE(CAST(AVG(CASE WHEN j.status IN ('starting','downloading','verifying') THEN j.progress END) AS INTEGER),0),MAX(j.updated_at),COALESCE((SELECT ji.camera_name FROM job_items ji JOIN jobs active_job ON active_job.id=ji.job_id JOIN nvrs active_nvr ON active_nvr.id=active_job.nvr_id WHERE active_nvr.site_id=s.id AND ji.status IN ('downloading','remuxing') ORDER BY ji.updated_at DESC LIMIT 1),'') FROM sites s LEFT JOIN nvrs n ON n.site_id=s.id LEFT JOIN jobs j ON j.nvr_id=n.id WHERE s.deleted_at IS NULL GROUP BY s.id ORDER BY CASE WHEN SUM(CASE WHEN j.status IN ('starting','downloading','verifying','queued') THEN 1 ELSE 0 END)>0 THEN 0 ELSE 1 END,s.customer,s.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SiteRetrievalStatus
	for rows.Next() {
		var v SiteRetrievalStatus
		if err = rows.Scan(&v.SiteID, &v.Customer, &v.SiteName, &v.ActiveJobs, &v.QueuedJobs, &v.FailedJobs, &v.CompletedJobs, &v.Progress, &v.UpdatedAt, &v.CurrentCamera); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) RetryJob(ctx context.Context, jobID int64, channelID string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	query := `UPDATE job_items SET status='pending',progress=0,bytes_downloaded=0,error='',output_path='',updated_at=CURRENT_TIMESTAMP WHERE job_id=?`
	args := []any{jobID}
	if channelID != "" {
		query += ` AND channel_id=?`
		args = append(args, channelID)
	}
	r, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	count, _ := r.RowsAffected()
	if count == 0 {
		return fmt.Errorf("no matching camera recordings to retry")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE jobs SET status='queued',progress=0,error='',output_files_json='[]',updated_at=CURRENT_TIMESTAMP WHERE id=?`, jobID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RetrySite(ctx context.Context, siteID int64) (int64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE job_items SET status='pending',progress=0,bytes_downloaded=0,error='',output_path='',updated_at=CURRENT_TIMESTAMP WHERE job_id IN (SELECT j.id FROM jobs j JOIN nvrs n ON n.id=j.nvr_id WHERE n.site_id=? AND j.status='failed')`, siteID); err != nil {
		return 0, err
	}
	r, err := tx.ExecContext(ctx, `UPDATE jobs SET status='queued',progress=0,error='',output_files_json='[]',updated_at=CURRENT_TIMESTAMP WHERE id IN (SELECT j.id FROM jobs j JOIN nvrs n ON n.id=j.nvr_id WHERE n.site_id=? AND j.status='failed')`, siteID)
	if err != nil {
		return 0, err
	}
	count, _ := r.RowsAffected()
	return count, tx.Commit()
}

func (s *Store) GetSetting(ctx context.Context, key, defaultValue string) string {
	var value string
	if err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&value); err != nil {
		return defaultValue
	}
	return value
}
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES(?,?,CURRENT_TIMESTAMP) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=CURRENT_TIMESTAMP`, key, value)
	return err
}

func (s *Store) ValidateNVRInSite(ctx context.Context, nvrID, siteID int64) error {
	var found int64
	err := s.DB.QueryRowContext(ctx, `SELECT n.site_id FROM nvrs n JOIN sites s ON s.id=n.site_id WHERE n.id=? AND s.deleted_at IS NULL`, nvrID).Scan(&found)
	if err != nil {
		return err
	}
	if found != siteID {
		return fmt.Errorf("recorder does not belong to selected site")
	}
	return nil
}

func ActiveJobStatus(status string) bool {
	return strings.Contains(" starting downloading verifying ", " "+status+" ")
}
