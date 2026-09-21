package library

import (
	"context"
	"database/sql"
	"fmt"
	"path"
	"path/filepath"
)

// LookupPlaylistIDByPath returns Navidrome's internal playlist ID for the
// playlist backed by the .m3u8 at localM3U8Path, by reconstructing the
// path Navidrome stores for it (its library root prefix, e.g. "/music",
// plus the file's path relative to musicPath) and matching it against the
// playlist table. The bool return is false (with a nil error) if Navidrome
// hasn't scanned the file in yet — not a failure, just "not yet"; callers
// matching right after writing a new file should poll.
func LookupPlaylistIDByPath(ctx context.Context, dbPath, musicPath, localM3U8Path string) (string, bool, error) {
	db, cleanup, err := openDB(ctx, dbPath)
	if err != nil {
		return "", false, err
	}
	defer cleanup()

	var libraryRoot string
	if err := db.QueryRowContext(ctx, `SELECT path FROM library ORDER BY id LIMIT 1`).Scan(&libraryRoot); err != nil {
		return "", false, fmt.Errorf("looking up Navidrome library root: %w", err)
	}

	rel, err := filepath.Rel(musicPath, localM3U8Path)
	if err != nil {
		return "", false, fmt.Errorf("computing path relative to music root: %w", err)
	}
	wantPath := path.Join(libraryRoot, filepath.ToSlash(rel))

	var id string
	err = db.QueryRowContext(ctx, `SELECT id FROM playlist WHERE path = ? ORDER BY updated_at DESC LIMIT 1`, wantPath).Scan(&id)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("looking up playlist by path: %w", err)
	}
	return id, true, nil
}
