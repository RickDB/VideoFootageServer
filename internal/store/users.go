package store

import "context"

// ListUsers returns every user (including disabled ones, so the admin UI can
// still see and re-enable them) with their customer name for display.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+userColumns+` FROM users u JOIN customers cu ON cu.id=u.customer_id ORDER BY u.disabled,u.username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := scanUser(rows, &u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// AdminResetPassword sets a new hash and forces a change on next sign-in —
// unlike UpdateUserPassword (used when a user changes their own password),
// which clears must_change_password since they just proved they know it.
func (s *Store) AdminResetPassword(ctx context.Context, userID int64, passwordHash string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE users SET password_hash=?,must_change_password=1,updated_at=CURRENT_TIMESTAMP WHERE id=?`, passwordHash, userID)
	return err
}

func (s *Store) GetUser(ctx context.Context, id int64) (User, error) {
	var u User
	err := scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users u JOIN customers cu ON cu.id=u.customer_id WHERE u.id=?`, id), &u)
	return u, err
}

// CreateUserFull creates a user with the full RBAC shape (role, customer,
// site scoping). The bootstrap admin still goes through auth.Manager's
// simpler CreateUser; this is for everyone created afterwards via the Users
// admin page.
func (s *Store) CreateUserFull(ctx context.Context, u User) (int64, error) {
	if u.CustomerID < 1 {
		u.CustomerID = 1
	}
	if u.RequestScope == "" {
		u.RequestScope = "own"
	}
	r, err := s.DB.ExecContext(ctx, `INSERT INTO users(username,password_hash,role,must_change_password,customer_id,all_sites,request_scope,disabled) VALUES(?,?,?,?,?,?,?,?)`,
		u.Username, u.PasswordHash, u.Role, u.MustChangePassword, u.CustomerID, u.AllSites, u.RequestScope, u.Disabled)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

// UpdateUser changes role/customer/scoping/disabled state, not the password
// (ChangePassword/UpdateUserPassword own that, so a reset always goes
// through the bcrypt path).
func (s *Store) UpdateUser(ctx context.Context, u User) error {
	if u.CustomerID < 1 {
		u.CustomerID = 1
	}
	if u.RequestScope == "" {
		u.RequestScope = "own"
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE users SET role=?,customer_id=?,all_sites=?,request_scope=?,disabled=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		u.Role, u.CustomerID, u.AllSites, u.RequestScope, u.Disabled, u.ID)
	return err
}

// SetUserSiteAccess replaces the explicit set of sites a non-all-sites user
// can see and submit requests for.
func (s *Store) SetUserSiteAccess(ctx context.Context, userID int64, siteIDs []int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM user_site_access WHERE user_id=?`, userID); err != nil {
		return err
	}
	for _, siteID := range siteIDs {
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO user_site_access(user_id,site_id) VALUES(?,?)`, userID, siteID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListUserSiteAccess(ctx context.Context, userID int64) ([]int64, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT site_id FROM user_site_access WHERE user_id=?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// UserCanAccessSite is the IDOR check: an admin can access every site; a
// site-scoped user can access only sites within their own customer that are
// either explicitly granted or covered by AllSites.
func (s *Store) UserCanAccessSite(ctx context.Context, u User, siteID int64) (bool, error) {
	if u.Role == "admin" {
		return true, nil
	}
	site, err := s.GetSite(ctx, siteID)
	if err != nil {
		return false, err
	}
	if site.CustomerID != u.CustomerID {
		return false, nil
	}
	if u.AllSites {
		return true, nil
	}
	var n int
	err = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_site_access WHERE user_id=? AND site_id=?`, u.ID, siteID).Scan(&n)
	return n > 0, err
}

// ListSitesForUser returns the sites a user is allowed to see: every site
// for an admin, and only the caller's own customer's sites (filtered further
// to the explicit grant list unless AllSites is set) for anyone else.
func (s *Store) ListSitesForUser(ctx context.Context, u User) ([]Site, error) {
	if u.Role == "admin" {
		return s.ListSites(ctx)
	}
	sites, err := s.ListSitesForCustomer(ctx, u.CustomerID)
	if err != nil {
		return nil, err
	}
	if u.AllSites {
		return sites, nil
	}
	allowed, err := s.ListUserSiteAccess(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	allowedSet := make(map[int64]bool, len(allowed))
	for _, id := range allowed {
		allowedSet[id] = true
	}
	out := sites[:0]
	for _, site := range sites {
		if allowedSet[site.ID] {
			out = append(out, site)
		}
	}
	return out, nil
}
