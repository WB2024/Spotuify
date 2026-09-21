package library

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// openDB opens dbPath read-only. Navidrome databases are frequently
// deployed inside Docker on a NAS, where the live file can be owned by a
// different UID than the one running Spotuify, or briefly locked while
// Navidrome itself is writing — both surface as an opaque disk-I/O style
// error from SQLite rather than a clear "permission denied". Rather than
// fail outright, this falls back to a point-in-time copy in a temp
// directory and opens that instead.
//
// The copy includes the WAL and SHM sidecar files alongside the main
// database file, not just the .db file itself: Navidrome runs SQLite in
// WAL mode, where recently-written data can sit in the (potentially very
// large) -wal file for a long time before being checkpointed back into the
// main file. Copying only the .db file risks silently working from a
// stale snapshot missing whatever hasn't been checkpointed yet.
//
// Returns the opened *sql.DB and a cleanup func that closes it and removes
// any temp copy — always call it, even on error paths that don't reach it
// (there are none: on error this returns before creating anything to clean
// up).
func openDB(ctx context.Context, dbPath string) (*sql.DB, func(), error) {
	db, directErr := tryOpen(ctx, dbPath)
	if directErr == nil {
		return db, func() { db.Close() }, nil
	}

	tmpDir, copyErr := copyDatabaseToTemp(dbPath)
	if copyErr != nil {
		return nil, nil, fmt.Errorf(
			"opening Navidrome database at %s: %w (also failed to fall back to a local copy: %v)",
			dbPath, directErr, copyErr,
		)
	}

	tmpDBPath := filepath.Join(tmpDir, filepath.Base(dbPath))
	db, err := tryOpen(ctx, tmpDBPath)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, nil, fmt.Errorf(
			"opening Navidrome database at %s: %w (fell back to a local copy, but that failed too: %v)",
			dbPath, directErr, err,
		)
	}

	cleanup := func() {
		db.Close()
		os.RemoveAll(tmpDir)
	}
	return db, cleanup, nil
}

func tryOpen(ctx context.Context, dbPath string) (*sql.DB, error) {
	dsn := fmt.Sprintf("%s?_query_only=1&_busy_timeout=5000", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// copyDatabaseToTemp copies dbPath, and its -wal/-shm sidecars if present,
// into a fresh temp directory, returning that directory's path.
func copyDatabaseToTemp(dbPath string) (string, error) {
	tmpDir, err := os.MkdirTemp("", "spotuify-navidrome-*")
	if err != nil {
		return "", err
	}

	paths := []string{dbPath, dbPath + "-wal", dbPath + "-shm"}
	copiedMain := false
	for _, p := range paths {
		dst := filepath.Join(tmpDir, filepath.Base(p))
		if err := copyFile(p, dst); err != nil {
			if os.IsNotExist(err) && p != dbPath {
				continue // sidecar files are optional (non-WAL mode has none)
			}
			os.RemoveAll(tmpDir)
			return "", err
		}
		if p == dbPath {
			copiedMain = true
		}
	}
	if !copiedMain {
		os.RemoveAll(tmpDir)
		return "", fmt.Errorf("could not copy %s", dbPath)
	}
	return tmpDir, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
