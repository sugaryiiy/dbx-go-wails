package server

import (
	"context"
	"errors"
)

// Wails binds these methods after a native picker grant. HTTP never accepts paths.
func (s *Server) SQLiteBackup(ctx context.Context, id, path string) error {
	if !s.desktop {
		return errors.New("native backup requires desktop mode")
	}
	return s.database.SQLiteBackup(ctx, id, path)
}
func (s *Server) SQLiteRestore(ctx context.Context, id, path string) error {
	if !s.desktop {
		return errors.New("native restore requires desktop mode")
	}
	return s.database.SQLiteRestore(ctx, id, path)
}
