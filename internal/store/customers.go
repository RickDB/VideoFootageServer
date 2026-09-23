package store

import "context"

// Customer is a tenant of the platform. "Internal Operations" (id 1) is the
// default owner of pre-existing/operational data; every other customer is a
// request-only client such as Collins, with its own branding and secured
// footage retention default.
type Customer struct {
	ID                                     int64
	Name, Slug, Country                    string
	ThemePrimary, ThemeSecondary, LogoText string
	SecuredRetentionDays                   int
}

func (s *Store) ListCustomers(ctx context.Context) ([]Customer, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,name,slug,country,theme_primary,theme_secondary,logo_text,secured_retention_days FROM customers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Customer
	for rows.Next() {
		var c Customer
		if err := rows.Scan(&c.ID, &c.Name, &c.Slug, &c.Country, &c.ThemePrimary, &c.ThemeSecondary, &c.LogoText, &c.SecuredRetentionDays); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetCustomer(ctx context.Context, id int64) (Customer, error) {
	var c Customer
	err := s.DB.QueryRowContext(ctx, `SELECT id,name,slug,country,theme_primary,theme_secondary,logo_text,secured_retention_days FROM customers WHERE id=?`, id).
		Scan(&c.ID, &c.Name, &c.Slug, &c.Country, &c.ThemePrimary, &c.ThemeSecondary, &c.LogoText, &c.SecuredRetentionDays)
	return c, err
}

func (s *Store) CreateCustomer(ctx context.Context, c Customer) (int64, error) {
	if c.SecuredRetentionDays < 1 {
		c.SecuredRetentionDays = 32
	}
	r, err := s.DB.ExecContext(ctx, `INSERT INTO customers(name,slug,country,theme_primary,theme_secondary,logo_text,secured_retention_days) VALUES(?,?,?,?,?,?,?)`,
		c.Name, c.Slug, c.Country, c.ThemePrimary, c.ThemeSecondary, c.LogoText, c.SecuredRetentionDays)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

func (s *Store) UpdateCustomer(ctx context.Context, c Customer) error {
	if c.SecuredRetentionDays < 1 {
		c.SecuredRetentionDays = 32
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE customers SET name=?,slug=?,country=?,theme_primary=?,theme_secondary=?,logo_text=?,secured_retention_days=? WHERE id=?`,
		c.Name, c.Slug, c.Country, c.ThemePrimary, c.ThemeSecondary, c.LogoText, c.SecuredRetentionDays, c.ID)
	return err
}
