package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/RickDB/HikFootageServer/internal/store"
)

// --- User management (admin-only, gated by opOnly at the route level) ---

func (a *App) usersList(w http.ResponseWriter, r *http.Request) {
	users, err := a.store.ListUsers(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.render(w, r, "users", pageData{Title: "Users", Users: users, Notice: r.URL.Query().Get("notice")})
}

func (a *App) userForm(w http.ResponseWriter, r *http.Request) {
	customers, _ := a.store.ListCustomers(r.Context())
	sites, _ := a.store.ListSites(r.Context())
	a.render(w, r, "user_form", pageData{Title: "Add user", Customers: customers, Sites: sites, EditUser: store.User{Role: "admin", CustomerID: 1, AllSites: true, RequestScope: "own"}})
}

func formUser(r *http.Request) store.User {
	customerID, _ := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	return store.User{
		Username:     strings.TrimSpace(r.FormValue("username")),
		Role:         r.FormValue("role"),
		CustomerID:   customerID,
		AllSites:     r.FormValue("all_sites") == "on",
		RequestScope: r.FormValue("request_scope"),
		Disabled:     r.FormValue("disabled") == "on",
	}
}

func formUserSiteIDs(r *http.Request) []int64 {
	var out []int64
	for _, v := range r.Form["site_ids"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			out = append(out, id)
		}
	}
	return out
}

func validateUser(u store.User) error {
	if u.Username == "" {
		return fmt.Errorf("username is required")
	}
	if u.Role != "admin" && u.Role != "reviewer" && u.Role != "downloader" && u.Role != "collins" {
		return fmt.Errorf("unknown role")
	}
	if u.RequestScope != "own" && u.RequestScope != "sites" && u.RequestScope != "all" {
		return fmt.Errorf("unknown request scope")
	}
	if u.CustomerID < 1 {
		return fmt.Errorf("customer is required")
	}
	return nil
}

func (a *App) createUserHandler(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	u := formUser(r)
	if err := validateUser(u); err != nil {
		customers, _ := a.store.ListCustomers(r.Context())
		sites, _ := a.store.ListSites(r.Context())
		a.render(w, r, "user_form", pageData{Title: "Add user", Customers: customers, Sites: sites, EditUser: u, Error: err.Error()})
		return
	}
	password := r.FormValue("password")
	id, err := a.auth.CreateUserWithRole(r.Context(), u, password)
	if err != nil {
		customers, _ := a.store.ListCustomers(r.Context())
		sites, _ := a.store.ListSites(r.Context())
		a.render(w, r, "user_form", pageData{Title: "Add user", Customers: customers, Sites: sites, EditUser: u, Error: err.Error()})
		return
	}
	if !u.AllSites {
		_ = a.store.SetUserSiteAccess(r.Context(), id, formUserSiteIDs(r))
	}
	actor, _ := currentUser(r)
	_ = a.store.LogAudit(r.Context(), actor.Username, "user.created", "user", id, "username="+u.Username+" role="+u.Role)
	http.Redirect(w, r, fmt.Sprintf("/users/%d?notice=%s", id, url.QueryEscape("User created. Share the temporary password securely — they'll be asked to change it on first sign-in.")), http.StatusSeeOther)
}

func (a *App) userEdit(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	u, e := a.store.GetUser(r.Context(), id)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	access, _ := a.store.ListUserSiteAccess(r.Context(), id)
	accessSet := make(map[int64]bool, len(access))
	for _, siteID := range access {
		accessSet[siteID] = true
	}
	customers, _ := a.store.ListCustomers(r.Context())
	sites, _ := a.store.ListSites(r.Context())
	a.render(w, r, "user_form", pageData{Title: "Edit user", Customers: customers, Sites: sites, EditUser: u, UserSiteIDs: accessSet, Notice: r.URL.Query().Get("notice")})
}

func (a *App) updateUserHandler(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	actor, _ := currentUser(r)
	if id == actor.ID && r.FormValue("disabled") == "on" {
		http.Redirect(w, r, fmt.Sprintf("/users/%d?notice=%s", id, url.QueryEscape("You can't disable your own account.")), http.StatusSeeOther)
		return
	}
	_ = r.ParseForm()
	u := formUser(r)
	u.ID = id
	if err := validateUser(u); err != nil {
		customers, _ := a.store.ListCustomers(r.Context())
		sites, _ := a.store.ListSites(r.Context())
		a.render(w, r, "user_form", pageData{Title: "Edit user", Customers: customers, Sites: sites, EditUser: u, Error: err.Error()})
		return
	}
	if err := a.store.UpdateUser(r.Context(), u); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !u.AllSites {
		_ = a.store.SetUserSiteAccess(r.Context(), id, formUserSiteIDs(r))
	}
	notice := "User settings saved."
	if newPassword := r.FormValue("new_password"); newPassword != "" {
		if err := a.auth.ResetPassword(r.Context(), id, newPassword); err != nil {
			a.render(w, r, "user_form", pageData{Title: "Edit user", EditUser: u, Error: err.Error()})
			return
		}
		notice = "User settings saved and a new temporary password was set."
	}
	_ = a.store.LogAudit(r.Context(), actor.Username, "user.updated", "user", id, "username="+u.Username+" role="+u.Role)
	http.Redirect(w, r, fmt.Sprintf("/users/%d?notice=%s", id, url.QueryEscape(notice)), http.StatusSeeOther)
}

// --- Audit trail (admin-only) ---

func (a *App) auditPage(w http.ResponseWriter, r *http.Request) {
	entries, err := a.store.ListAudit(r.Context(), 500)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.render(w, r, "audit", pageData{Title: "Audit trail", Audit: entries})
}

// --- Per-customer branding (admin-only) ---

func (a *App) customersPage(w http.ResponseWriter, r *http.Request) {
	customers, err := a.store.ListCustomers(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.render(w, r, "customers", pageData{Title: "Customers & branding", Customers: customers, Notice: r.URL.Query().Get("notice")})
}

func (a *App) updateCustomerHandler(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	c, e := a.store.GetCustomer(r.Context(), id)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	_ = r.ParseForm()
	c.Name = strings.TrimSpace(r.FormValue("name"))
	c.Country = strings.TrimSpace(r.FormValue("country"))
	c.ThemePrimary = strings.TrimSpace(r.FormValue("theme_primary"))
	c.ThemeSecondary = strings.TrimSpace(r.FormValue("theme_secondary"))
	c.LogoText = strings.TrimSpace(r.FormValue("logo_text"))
	c.SecuredRetentionDays, _ = strconv.Atoi(r.FormValue("secured_retention_days"))
	if c.Name == "" {
		http.Redirect(w, r, "/settings/customers?notice="+url.QueryEscape("Customer name is required."), http.StatusSeeOther)
		return
	}
	if err := a.store.UpdateCustomer(r.Context(), c); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings/customers?notice="+url.QueryEscape(c.Name+" branding saved."), http.StatusSeeOther)
}
