package compliance

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

type Target struct {
	SiteID, NVRID                   int64
	Site, Recorder, Camera, Channel string
}
type RecorderSummary struct {
	NVRID                                                        int64
	Name, Status, Scope                                          string
	Expected, Passed, Failed, Errors, Skipped, Exported, Pending int
	Results                                                      []Result
}
type SiteSummary struct {
	SiteID, RunID                                      int64
	Name, Kind, Status, Action                         string
	CheckedAt                                          time.Time
	Passed, Failed, Errors, Skipped, Exported, Pending int
	Recorders                                          []RecorderSummary
}
type RunView struct {
	Run
	Sites []SiteSummary
}
type CheckSummary struct {
	ID                                      int64
	Kind, Reference, Status                 string
	Created                                 time.Time
	Passed, Failed, Errors, Review, Pending int
}
type Overview struct {
	Sites     []SiteSummary
	History   []CheckSummary
	Active    []Run
	Schedules []Schedule
}

func View(r Run) RunView { return RunView{r, Summarize(r)} }

// Summarize reports a site as passing only after all its expected cameras finish.
// Recorder-level search failures never count as proof of missing retention.
func Summarize(r Run) []SiteSummary {
	sites := map[int64]*SiteSummary{}
	recorders := map[int64]map[string]*RecorderSummary{}
	getSite := func(id int64, name string) *SiteSummary {
		if s := sites[id]; s != nil {
			if name != "" {
				s.Name = name
			}
			return s
		}
		s := &SiteSummary{SiteID: id, RunID: r.ID, Name: name, Kind: r.Kind, CheckedAt: r.Created}
		sites[id] = s
		recorders[id] = map[string]*RecorderSummary{}
		return s
	}
	getRecorder := func(site int64, id int64, name string) *RecorderSummary {
		key := fmt.Sprintf("%d:%s", id, name)
		if n := recorders[site][key]; n != nil {
			return n
		}
		n := &RecorderSummary{NVRID: id, Name: name}
		recorders[site][key] = n
		return n
	}
	for _, id := range r.SiteIDs {
		getSite(id, "")
	}
	for _, t := range r.Targets {
		getSite(t.SiteID, t.Site)
		n := getRecorder(t.SiteID, t.NVRID, t.Recorder)
		n.Expected++
	}
	for _, v := range r.Results {
		s := getSite(v.SiteID, v.Site)
		n := getRecorder(v.SiteID, v.NVRID, v.Recorder)
		n.Results = append(n.Results, v)
		switch v.Status {
		case "pass":
			n.Passed++
			s.Passed++
		case "fail":
			n.Failed++
			s.Failed++
		case "error":
			n.Errors++
			s.Errors++
		case "skipped":
			n.Skipped++
			s.Skipped++
		case "exported":
			n.Exported++
			s.Exported++
		}
	}
	terminal := r.Status == "completed" || r.Status == "completed_with_issues"
	out := []SiteSummary{}
	for id, s := range sites {
		for _, n := range recorders[id] {
			if n.Expected == 0 {
				n.Expected = len(n.Results)
			}
			n.Pending = max(0, n.Expected-len(n.Results))
			n.Scope = "camera"
			switch {
			case n.Failed > 0:
				n.Status = "fail"
				if n.Failed == n.Expected && n.Pending == 0 && n.Errors == 0 {
					n.Scope = "entire_nvr"
				}
			case n.Errors > 0:
				n.Status = "error"
				for _, v := range n.Results {
					if v.Channel == "" {
						n.Scope = "recorder"
					}
				}
			case n.Pending > 0:
				n.Status = "pending"
			case n.Skipped > 0:
				n.Status = "review"
			case n.Passed+n.Exported > 0:
				n.Status = "pass"
			default:
				n.Status = "unknown"
			}
			// Old/incomplete runs have no target manifest, so cannot prove completion.
			if len(r.Targets) == 0 && !terminal && n.Status == "pass" {
				n.Status = "pending"
				n.Pending++
			}
			s.Pending += n.Pending
			s.Recorders = append(s.Recorders, *n)
		}
		sort.Slice(s.Recorders, func(i, j int) bool { return s.Recorders[i].Name < s.Recorders[j].Name })
		switch {
		case s.Failed > 0:
			s.Status = "fail"
			s.Action = "Restore 28-day recording availability"
		case s.Errors > 0:
			s.Status = "error"
			s.Action = "Resolve connection, discovery, or recorder errors"
		case s.Pending > 0:
			s.Status = "pending"
			s.Action = "Check is still incomplete"
		case s.Skipped > 0:
			s.Status = "review"
			s.Action = "Review dark or placeholder camera frames"
		case s.Passed+s.Exported > 0:
			s.Status = "pass"
			s.Action = "No action required"
		default:
			s.Status = "unknown"
			s.Action = "Run a check to establish coverage"
		}
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (m *Manager) Overview(ctx context.Context) (Overview, error) {
	o := Overview{Sites: []SiteSummary{}, History: []CheckSummary{}, Active: []Run{}}
	configured, err := m.Store.ListSites(ctx)
	if err != nil {
		return o, err
	}
	names := map[int64]string{}
	for _, s := range configured {
		names[s.ID] = s.Name
	}
	rows, err := m.Store.DB.QueryContext(ctx, "SELECT id,data,status FROM compliance_runs ORDER BY id DESC")
	if err != nil {
		return o, err
	}
	latest := map[string]SiteSummary{}
	for rows.Next() {
		var id int64
		var data, status string
		if err = rows.Scan(&id, &data, &status); err != nil {
			rows.Close()
			return o, err
		}
		var r Run
		if err = json.Unmarshal([]byte(data), &r); err != nil {
			rows.Close()
			return o, err
		}
		r.ID = id
		r.Status = status
		if status == "running" || status == "queued" {
			o.Active = append(o.Active, r)
		}
		sum := Summarize(r)
		h := CheckSummary{ID: r.ID, Kind: r.Kind, Reference: r.Reference, Created: r.Created, Status: r.Status}
		for _, s := range sum {
			switch s.Status {
			case "pass":
				h.Passed++
			case "fail":
				h.Failed++
			case "error":
				h.Errors++
			case "review":
				h.Review++
			default:
				h.Pending++
			}
			name, exists := names[s.SiteID]
			if !exists {
				continue
			}
			s.Name = name
			key := fmt.Sprintf("%s:%d", s.Kind, s.SiteID)
			if _, ok := latest[key]; !ok {
				latest[key] = s
			}
		}
		o.History = append(o.History, h)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return o, err
	}
	for _, s := range configured {
		for _, kind := range []string{"retention", "snapshots"} {
			key := fmt.Sprintf("%s:%d", kind, s.ID)
			v, ok := latest[key]
			if !ok {
				v = SiteSummary{SiteID: s.ID, Name: s.Name, Kind: kind, Status: "unknown", Action: "Run a check to establish coverage", Recorders: []RecorderSummary{}}
			}
			o.Sites = append(o.Sites, v)
		}
	}
	o.Schedules, err = m.Schedules(ctx)
	return o, err
}
