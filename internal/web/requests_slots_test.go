package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/RickDB/HikFootageServer/internal/store"
)

// TestCreateRequestPersistsMultipleSlots covers the request form's "add
// another time range" feature end to end: posting more than one date/time
// range in slots_json must store all of them (Slots()), while
// IncidentDate/StartTime/EndTime keep mirroring the first one so older
// code (sorting, the requests table, ListFootageRequests) still works
// unchanged.
func TestCreateRequestPersistsMultipleSlots(t *testing.T) {
	server, s := testServer(t)
	defer s.Close()
	ctx := context.Background()

	siteID, err := s.CreateSite(ctx, store.Site{Name: "HQ", CustomerID: 1, Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	cookie := loginAsOperator(t, server, s)

	form := url.Values{
		"site_id":      {strconv.FormatInt(siteID, 10)},
		"cameras_json": {`[{"id":1,"channel_id":"101","name":"Front door"}]`},
		"slots_json":   {`[{"date":"2026-01-01","start":"10:00","end":"11:00"},{"date":"2026-01-03","start":"09:00","end":"09:30"}]`},
		"reference":    {"INC-1"},
	}
	request := httptest.NewRequest(http.MethodPost, "/requests", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("create request status = %d, body=%s", response.Code, response.Body.String())
	}
	loc := response.Header().Get("Location")
	idStr := strings.SplitN(strings.TrimPrefix(loc, "/requests/"), "?", 2)[0]
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		t.Fatalf("couldn't parse request id from redirect %q: %v", loc, err)
	}

	req, err := s.GetFootageRequest(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if req.IncidentDate != "2026-01-01" || req.StartTime != "10:00" || req.EndTime != "11:00" {
		t.Fatalf("first slot not mirrored into legacy columns: %+v", req)
	}
	slots := req.Slots()
	if len(slots) != 2 {
		t.Fatalf("Slots() = %+v, want 2 entries", slots)
	}
	if slots[1].Date != "2026-01-03" || slots[1].Start != "09:00" || slots[1].End != "09:30" {
		t.Fatalf("second slot = %+v, want 2026-01-03 09:00-09:30", slots[1])
	}
}

// TestFootageRequestSlotsFallsBackForLegacyRows makes sure a row written
// before slots_json existed (or with it left empty) still reports its one
// incident date/time through Slots(), rather than an empty list.
func TestFootageRequestSlotsFallsBackForLegacyRows(t *testing.T) {
	req := store.FootageRequest{IncidentDate: "2026-02-02", StartTime: "08:00", EndTime: "08:30"}
	slots := req.Slots()
	if len(slots) != 1 || slots[0].Date != "2026-02-02" || slots[0].Start != "08:00" || slots[0].End != "08:30" {
		t.Fatalf("Slots() fallback = %+v, want a single 2026-02-02 08:00-08:30 slot", slots)
	}

	empty := store.FootageRequest{}
	if got := empty.Slots(); len(got) != 0 {
		t.Fatalf("Slots() on an empty request = %+v, want none", got)
	}
}
