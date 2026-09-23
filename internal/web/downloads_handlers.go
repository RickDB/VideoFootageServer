package web

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/RickDB/HikFootageServer/internal/safepath"
	"github.com/RickDB/HikFootageServer/internal/store"
)

// downloadJobView pairs a completed job with the items (cameras) it
// produced, pre-loaded once for the Downloads page rather than fetched
// per-row client-side.
type downloadJobView struct {
	Job   store.Job
	Items []store.JobItem
}

// loadDownloadJobs lists the completed jobs a user may download from —
// every completed job for CanDownloadAll, or only the ones linked to
// footage requests they personally submitted otherwise — and loads each
// job's items alongside it.
func (a *App) loadDownloadJobs(r *http.Request, u store.User) ([]downloadJobView, error) {
	var jobs []store.Job
	var err error
	if u.CanDownloadAll() {
		jobs, err = a.store.ListCompletedJobs(r.Context(), 100)
	} else {
		jobs, err = a.store.ListCompletedJobsForRequester(r.Context(), u.ID, 100)
	}
	if err != nil {
		return nil, err
	}
	out := make([]downloadJobView, 0, len(jobs))
	for _, j := range jobs {
		items, _ := a.store.ListJobItems(r.Context(), j.ID)
		out = append(out, downloadJobView{Job: j, Items: items})
	}
	return out, nil
}

func (a *App) downloadsPage(w http.ResponseWriter, r *http.Request) {
	u, ok := currentUser(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	views, err := a.loadDownloadJobs(r, u)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.render(w, r, "downloads", pageData{Title: "Downloads", DownloadJobs: views})
}

// canAccessJobDownload is the IDOR check shared by both download endpoints:
// CanDownloadAll sees every completed job, anyone else only jobs linked to
// a footage request they themselves submitted.
func (a *App) canAccessJobDownload(r *http.Request, u store.User, jobID int64) (bool, error) {
	if u.CanDownloadAll() {
		return true, nil
	}
	return a.store.JobLinkedToRequester(r.Context(), jobID, u.ID)
}

// downloadJobZip streams every completed camera's output file for a job as
// a single zip, built on the fly rather than staged to disk first.
func (a *App) downloadJobZip(w http.ResponseWriter, r *http.Request) {
	u, ok := currentUser(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	id, e := idParam(r)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	job, e := a.store.GetJob(r.Context(), id)
	if e != nil || job.Status != "completed" {
		http.NotFound(w, r)
		return
	}
	allowed, e := a.canAccessJobDownload(r, u, id)
	if e != nil {
		http.Error(w, e.Error(), http.StatusInternalServerError)
		return
	}
	if !allowed {
		http.Error(w, "not authorized to download this job", http.StatusForbidden)
		return
	}
	files, e := a.store.CompletedItemFiles(r.Context(), id)
	if e != nil {
		http.Error(w, e.Error(), http.StatusInternalServerError)
		return
	}
	if len(files) == 0 {
		http.Error(w, "no files available for this job", http.StatusNotFound)
		return
	}
	zipName := safepath.Component(job.CaseRef, fmt.Sprintf("job-%d", job.ID)) + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+zipName+`"`)
	zw := zip.NewWriter(w)
	defer zw.Close()
	used := map[string]int{}
	for _, path := range files {
		if e := addFileToZip(zw, path, used); e != nil {
			a.log.Error("zip job file", "job", id, "path", path, "error", e)
			continue
		}
	}
	_ = a.store.LogAudit(r.Context(), u.Username, "download.zip", "job", id, fmt.Sprintf("%d file(s)", len(files)))
}

func addFileToZip(zw *zip.Writer, path string, used map[string]int) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	name := safepath.Component(filepath.Base(path), "file")
	if n := used[name]; n > 0 {
		ext := filepath.Ext(name)
		base := name[:len(name)-len(ext)]
		name = fmt.Sprintf("%s (%d)%s", base, n, ext)
	}
	used[filepath.Base(path)]++
	hdr, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	hdr.Name = name
	hdr.Method = zip.Deflate
	out, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, f)
	return err
}

// downloadJobItem serves a single camera's output file for a completed job,
// via http.ServeContent so range/conditional requests work for free.
func (a *App) downloadJobItem(w http.ResponseWriter, r *http.Request) {
	u, ok := currentUser(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	jobID, e := idParam(r)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	itemID, e := strconv.ParseInt(r.PathValue("item"), 10, 64)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	job, e := a.store.GetJob(r.Context(), jobID)
	if e != nil || job.Status != "completed" {
		http.NotFound(w, r)
		return
	}
	allowed, e := a.canAccessJobDownload(r, u, jobID)
	if e != nil {
		http.Error(w, e.Error(), http.StatusInternalServerError)
		return
	}
	if !allowed {
		http.Error(w, "not authorized to download this job", http.StatusForbidden)
		return
	}
	item, e := a.store.GetJobItem(r.Context(), itemID)
	if e != nil || item.JobID != jobID || item.Status != "completed" || item.OutputPath == "" {
		http.NotFound(w, r)
		return
	}
	f, e := os.Open(item.OutputPath)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		http.Error(w, e.Error(), http.StatusInternalServerError)
		return
	}
	name := safepath.Component(item.CameraName, fmt.Sprintf("camera-%d", item.ID)) + filepath.Ext(item.OutputPath)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	_ = a.store.LogAudit(r.Context(), u.Username, "download.file", "job_item", item.ID, item.CameraName)
	http.ServeContent(w, r, name, info.ModTime(), f)
}
