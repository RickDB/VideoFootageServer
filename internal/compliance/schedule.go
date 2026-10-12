package compliance

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

type Schedule struct {
	configData                                              string
	ID                                                      int64
	Name, Kind, Mode, StartDate, StartTime, Timezone, Actor string
	Interval                                                int
	Weekdays                                                []int
	AllSites                                                bool
	SiteIDs                                                 []int64
	Active                                                  bool
	NextDue                                                 time.Time
	LastRunID                                               int64
	LastError                                               string
}

func (s Schedule) Validate() error {
	if s.Name == "" || len(s.Name) > 120 {
		return fmt.Errorf("schedule name must contain 1–120 characters")
	}
	if s.Kind != "retention" && s.Kind != "snapshots" {
		return fmt.Errorf("invalid check type")
	}
	if s.Mode != "daily" && s.Mode != "weekly" {
		return fmt.Errorf("choose daily or weekly interval")
	}
	limit := 365
	if s.Mode == "weekly" {
		limit = 52
	}
	if s.Interval < 1 || s.Interval > limit {
		return fmt.Errorf("interval must be between 1 and %d", limit)
	}
	if _, err := time.Parse("2006-01-02", s.StartDate); err != nil {
		return fmt.Errorf("invalid start date")
	}
	if _, err := time.Parse("15:04", s.StartTime); err != nil {
		return fmt.Errorf("invalid start time")
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil {
		return fmt.Errorf("invalid schedule timezone")
	}
	if s.Mode == "weekly" && len(s.Weekdays) == 0 {
		return fmt.Errorf("select at least one weekday")
	}
	seen := map[int]bool{}
	for _, d := range s.Weekdays {
		if d < 0 || d > 6 || seen[d] {
			return fmt.Errorf("invalid or duplicate weekday")
		}
		seen[d] = true
	}
	if !s.AllSites && len(s.SiteIDs) == 0 {
		return fmt.Errorf("select at least one installation")
	}
	return nil
}

// Next uses local calendar days/weeks, not elapsed 24h periods, across DST.
// A nonexistent local start time is skipped; repeated times run once per date.
func (s Schedule) Next(after time.Time) (time.Time, error) {
	if err := s.Validate(); err != nil {
		return time.Time{}, err
	}
	loc, _ := time.LoadLocation(s.Timezone)
	anchor, _ := time.Parse("2006-01-02", s.StartDate)
	clock, _ := time.Parse("15:04", s.StartTime)
	local := after.In(loc)
	date := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	if date.Before(anchor) {
		date = anchor
	}
	anchorMonday := anchor.AddDate(0, 0, -(int(anchor.Weekday())+6)%7)
	for i := 0; i < 4000; i++ {
		d := date.AddDate(0, 0, i)
		days := int(d.Sub(anchor).Hours() / 24)
		eligible := days >= 0 && days%s.Interval == 0
		if s.Mode == "weekly" {
			weeks := int(d.Sub(anchorMonday).Hours()/24) / 7
			eligible = days >= 0 && weeks%s.Interval == 0
			chosen := false
			for _, wd := range s.Weekdays {
				if int(d.Weekday()) == wd {
					chosen = true
				}
			}
			eligible = eligible && chosen
		}
		if !eligible {
			continue
		}
		candidate := time.Date(d.Year(), d.Month(), d.Day(), clock.Hour(), clock.Minute(), 0, 0, loc)
		if candidate.Hour() != clock.Hour() || candidate.Minute() != clock.Minute() || candidate.Day() != d.Day() {
			continue
		}
		if candidate.After(after) {
			return candidate.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("no scheduled occurrence found")
}
func (m *Manager) Schedules(ctx context.Context) ([]Schedule, error) {
	rows, err := m.Store.DB.QueryContext(ctx, "SELECT id,data,active,next_due,COALESCE(last_run_id,0),last_error FROM compliance_schedules ORDER BY id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Schedule{}
	for rows.Next() {
		var s Schedule
		var data, due string
		if err = rows.Scan(&s.ID, &data, &s.Active, &due, &s.LastRunID, &s.LastError); err != nil {
			return nil, err
		}
		id, active, last, lastErr := s.ID, s.Active, s.LastRunID, s.LastError
		if err = json.Unmarshal([]byte(data), &s); err != nil {
			return nil, err
		}
		s.configData = data
		s.ID = id
		s.Active = active
		s.LastRunID = last
		s.LastError = lastErr
		s.NextDue, err = time.Parse(time.RFC3339, due)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
func (m *Manager) SaveSchedule(ctx context.Context, s Schedule, now time.Time) (int64, error) {
	next, err := s.Next(now)
	if err != nil {
		return 0, err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return 0, err
	}
	tx, err := m.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if s.ID == 0 {
		res, e := tx.ExecContext(ctx, "INSERT INTO compliance_schedules(data,active,next_due) VALUES(?,?,?)", string(b), s.Active, next.Format(time.RFC3339))
		if e != nil {
			return 0, e
		}
		s.ID, err = res.LastInsertId()
	} else {
		res, e := tx.ExecContext(ctx, "UPDATE compliance_schedules SET data=?,active=?,next_due=?,last_error='' WHERE id=?", string(b), s.Active, next.Format(time.RFC3339), s.ID)
		if e != nil {
			return 0, e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return 0, sql.ErrNoRows
		}
	}
	if err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO audit_log(actor,action,entity_type,entity_id,details) VALUES(?,?,?,?,?)", s.Actor, "monitoring.schedule_saved", "monitoring_schedule", s.ID, string(b))
	if err != nil {
		return 0, err
	}
	return s.ID, tx.Commit()
}
func (m *Manager) ToggleSchedule(ctx context.Context, id int64, active bool, actor string, now time.Time) error {
	all, err := m.Schedules(ctx)
	if err != nil {
		return err
	}
	for _, s := range all {
		if s.ID == id {
			s.Active = active
			s.Actor = actor
			_, err = m.SaveSchedule(ctx, s, now)
			return err
		}
	}
	return sql.ErrNoRows
}
func (m *Manager) scheduleLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		if err := m.enqueueDue(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
			m.Log.Error("monitoring scheduler", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (m *Manager) enqueueDue(ctx context.Context, now time.Time) error {
	schedules, err := m.Schedules(ctx)
	if err != nil {
		return err
	}
	for _, s := range schedules {
		if !s.Active || s.NextDue.After(now) {
			continue
		}
		if err = m.enqueueSchedule(ctx, s, now); err != nil {
			return err
		}
	}
	return nil
}
func (m *Manager) enqueueSchedule(ctx context.Context, s Schedule, now time.Time) error {
	next, err := s.Next(now)
	if err != nil {
		return err
	}
	root, err := ResolveRoot(m.Config, m.Store.GetSetting(ctx, "compliance_root", "gdpr"))
	if err != nil {
		return m.scheduleError(ctx, s, err)
	}
	ids := s.SiteIDs
	if s.AllSites {
		sites, e := m.Store.ListSites(ctx)
		if e != nil {
			return e
		}
		ids = []int64{}
		for _, site := range sites {
			ids = append(ids, site.ID)
		}
	}
	if len(ids) == 0 {
		return m.scheduleError(ctx, s, fmt.Errorf("no installations available"))
	}
	tx, err := m.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var active bool
	var due, currentData string
	var previous sql.NullInt64
	if err = tx.QueryRowContext(ctx, "SELECT active,next_due,last_run_id,data FROM compliance_schedules WHERE id=?", s.ID).Scan(&active, &due, &previous, &currentData); err != nil {
		return err
	}
	if currentData != s.configData || !active || due != s.NextDue.Format(time.RFC3339) || s.NextDue.After(now) {
		return nil
	}
	if previous.Valid {
		var status string
		if err = tx.QueryRowContext(ctx, "SELECT status FROM compliance_runs WHERE id=?", previous.Int64).Scan(&status); err != nil {
			return err
		}
		if status == "queued" || status == "running" {
			_, err = tx.ExecContext(ctx, "UPDATE compliance_schedules SET next_due=?,last_error=? WHERE id=?", next.Format(time.RFC3339), "Previous scheduled check is still active; this occurrence was skipped.", s.ID)
			if err != nil {
				return err
			}
			return tx.Commit()
		}
	}
	r := Run{Kind: s.Kind, Reference: s.Name, Actor: s.Actor + " (scheduled)", Root: root, SiteIDs: ids, Status: "queued", Created: now, ScheduleID: s.ID, ScheduledFor: s.NextDue}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, "INSERT INTO compliance_runs(data,status) VALUES(?,'queued')", string(b))
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO compliance_occurrences(schedule_id,due,run_id) VALUES(?,?,?)", s.ID, due, id)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE compliance_schedules SET next_due=?,last_run_id=?,last_error='' WHERE id=?", next.Format(time.RFC3339), id, s.ID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO audit_log(actor,action,entity_type,entity_id,details) VALUES(?,?,?,?,?)", r.Actor, "monitoring.scheduled", "compliance_run", id, "Schedule "+strconv.FormatInt(s.ID, 10)+" due "+due)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (m *Manager) scheduleError(ctx context.Context, s Schedule, cause error) error {
	// Keep the due time so the scheduler retries after configuration is corrected.
	_, err := m.Store.DB.ExecContext(ctx, "UPDATE compliance_schedules SET last_error=? WHERE id=?", cause.Error(), s.ID)
	return err
}
