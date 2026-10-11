package database

import (
	"context"
	"database/sql"
	"errors"
	"io"
	sqlite "modernc.org/sqlite"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

func IsSQLiteFile(path string) (bool, error) {
	f, e := os.Open(path)
	if e != nil {
		return false, e
	}
	defer f.Close()
	header := make([]byte, 16)
	_, e = io.ReadFull(f, header)
	if errors.Is(e, io.EOF) || errors.Is(e, io.ErrUnexpectedEOF) {
		return false, nil
	}
	return string(header) == "SQLite format 3\x00", e
}

func stepSQLiteBackup(ctx context.Context, b *sqlite.Backup) error {
	for {
		if e := ctx.Err(); e != nil {
			return e
		}
		more, e := b.Step(128)
		if e != nil {
			var se *sqlite.Error
			if !errors.As(e, &se) || (se.Code() != 5 && se.Code() != 6) {
				return e
			}
			timer := time.NewTimer(10 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			continue
		}
		if !more {
			return nil
		}
	}
}
func distinctSQLiteFiles(source, target string) error {
	src, e := filepath.Abs(source)
	if e != nil {
		return e
	}
	dest, e := filepath.Abs(target)
	if e != nil {
		return e
	}
	if src == dest {
		return errors.New("source and destination refer to the same SQLite database")
	}
	if d, e := os.Lstat(dest); e == nil {
		if !d.Mode().IsRegular() {
			return errors.New("SQLite destination must be a regular file")
		}
		if s, e := os.Stat(src); e == nil && os.SameFile(s, d) {
			return errors.New("source and destination refer to the same SQLite database")
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	return nil
}
func (m *Manager) SQLiteBackup(ctx context.Context, id, destination string) error {
	db, c, _, e := m.pool(ctx, id, "")
	if e != nil {
		return e
	}
	if c.Type != "sqlite" {
		return errors.New("online backup requires SQLite")
	}
	if e = distinctSQLiteFiles(sqlitePath(c), destination); e != nil {
		return e
	}
	dest, e := filepath.Abs(destination)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(dest), ".dbx-sqlite-backup-*")
	if e != nil {
		return e
	}
	temp := f.Name()
	if e = f.Close(); e != nil {
		os.Remove(temp)
		return e
	}
	defer os.Remove(temp)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	conn, e := db.Conn(ctx)
	if e != nil {
		return e
	}
	defer conn.Close()
	e = conn.Raw(func(raw any) error {
		provider, ok := raw.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return errors.New("SQLite driver has no online backup API")
		}
		b, e := provider.NewBackup(temp)
		if e != nil {
			return e
		}
		stepErr := stepSQLiteBackup(ctx, b)
		return errors.Join(stepErr, b.Finish())
	})
	if e != nil {
		return e
	}
	f, e = os.OpenFile(temp, os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	syncErr := f.Sync()
	closeErr := f.Close()
	if e = errors.Join(syncErr, closeErr); e != nil {
		return e
	}
	return os.Rename(temp, dest)
}

// SQLite's backup transaction retains the destination on failed/cancelled copy.
func (m *Manager) SQLiteRestore(ctx context.Context, id, source string) error {
	valid, e := IsSQLiteFile(source)
	if e != nil {
		return e
	}
	if !valid {
		return errors.New("restore source is not a SQLite database")
	}
	_, c, locked, e := m.pool(ctx, id, "")
	if e != nil {
		return e
	}
	if c.Type != "sqlite" {
		return errors.New("online restore requires SQLite")
	}
	if locked {
		return errors.New("writes are locked for this connection")
	}
	if e = distinctSQLiteFiles(source, sqlitePath(c)); e != nil {
		return e
	}
	abs, e := filepath.Abs(source)
	if e != nil {
		return e
	}
	u := url.URL{Scheme: "file", Path: abs}
	q := u.Query()
	q.Set("mode", "ro")
	u.RawQuery = q.Encode()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	// Validate all pages before opening the destination for writing. A correct
	// header alone does not imply the backup is intact.
	check, e := sql.Open("sqlite", u.String())
	if e != nil {
		return e
	}
	rows, e := check.QueryContext(ctx, "PRAGMA quick_check")
	if e == nil {
		for rows.Next() {
			var result string
			if e = rows.Scan(&result); e != nil {
				break
			}
			if result != "ok" {
				e = errors.New("restore source failed SQLite integrity check: " + result)
				break
			}
		}
		if e == nil {
			e = rows.Err()
		}
		e = errors.Join(e, rows.Close())
	}
	e = errors.Join(e, check.Close())
	if e != nil {
		return e
	}
	// Remove and close every pool before restoring. Calls that fetched an old
	// pool fail once it closes, while new calls are rejected until completion.
	m.mu.Lock()
	current := m.connections[id]
	if current == nil || current.config.Type != "sqlite" || sqlitePath(current.config) != sqlitePath(c) {
		m.mu.Unlock()
		return errors.New("connection changed before SQLite restore")
	}
	if current.maintenanceCancel != nil {
		m.mu.Unlock()
		return errors.New("SQLite restore is already in progress")
	}
	if (current.config.ReadOnly || current.config.Production || len(current.config.ProductionDatabases) > 0) && !time.Now().Before(current.unlockedUntil) {
		m.mu.Unlock()
		return errors.New("writes are locked for this connection")
	}
	current.maintenanceCancel = cancel
	pools := current.pools
	current.pools = map[string]*sql.DB{}
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		current.maintenanceCancel = nil
		m.mu.Unlock()
	}()
	m.closeQuerySessions(id, "", "")
	for _, old := range pools {
		if e = old.Close(); e != nil {
			return e
		}
	}
	db, e := openDB(ctx, c, c.Database)
	if e != nil {
		return e
	}
	defer db.Close()
	conn, e := db.Conn(ctx)
	if e != nil {
		return e
	}
	defer conn.Close()
	return conn.Raw(func(raw any) error {
		provider, ok := raw.(interface {
			NewRestore(string) (*sqlite.Backup, error)
		})
		if !ok {
			return errors.New("SQLite driver has no online restore API")
		}
		b, e := provider.NewRestore(u.String())
		if e != nil {
			return e
		}
		stepErr := stepSQLiteBackup(ctx, b)
		return errors.Join(stepErr, b.Finish())
	})
}
