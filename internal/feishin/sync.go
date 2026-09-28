// Package feishin syncs playlist ordering into Feishin, the Navidrome
// client most of this project's README examples assume. Feishin's playlist
// list has no sort option for creation date - only name, recently added,
// recently played, or a manual drag-and-drop order (see its own Settings
// screen) - so making its sidebar chronological means driving that manual
// order directly, the same way dragging a playlist would.
package feishin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/syndtr/goleveldb/leveldb"

	"spotuify/internal/library"
)

// localStorageOrigin is the origin Feishin's renderer runs under. It's a
// packaged Electron app loading local files rather than a web page, so
// Chromium treats it as the file:// origin instead of a per-domain one.
const localStorageOrigin = "file://"

// SyncPlaylistOrder writes every playlist Navidrome has for ownerUsername,
// ordered by created_at (oldest first, unless descending is set), into
// Feishin's own manual sidebar order - the exact localStorage key
// ("playlist_order:<serverID>:owned") Feishin's own drag-and-drop
// reordering writes to (see sidebar-playlist-list.tsx in
// https://github.com/jeffvli/feishin). serverID is the nanoid Feishin
// assigned this Navidrome connection when it was added (Settings ›
// Feishin server ID); leveldbPath is Feishin's "Local Storage/leveldb"
// directory, part of its Chromium profile.
//
// Feishin must not be running when this is called - checkNotRunning (see
// lock.go) verifies that directly, against Chromium's own locking
// mechanism, before this ever opens the database. Do not remove that check
// on the assumption that LevelDB's single-writer guarantee makes it
// redundant: goleveldb enforces that guarantee with a different, mutually
// invisible OS lock than Chromium's own LevelDB does, so opening the
// database here would not, on its own, fail just because Feishin already
// has it open - see checkNotRunning's own doc comment for what actually
// happened when this package relied on that assumption.
func SyncPlaylistOrder(ctx context.Context, navidromeDBPath, ownerUsername, leveldbPath, serverID string, descending bool) error {
	if err := checkNotRunning(leveldbPath); err != nil {
		return err
	}

	ids, err := orderedPlaylistIDs(ctx, navidromeDBPath, ownerUsername, descending)
	if err != nil {
		return fmt.Errorf("reading playlist order from Navidrome: %w", err)
	}

	body, err := json.Marshal(ids)
	if err != nil {
		return err
	}

	db, err := leveldb.OpenFile(leveldbPath, nil)
	if err != nil {
		return fmt.Errorf("opening Feishin's local storage at %s: %w", leveldbPath, err)
	}
	defer db.Close()

	key := chromiumLocalStorageKey(fmt.Sprintf("playlist_order:%s:owned", serverID))
	// Chromium's DOM Storage value format: one leading byte marking the
	// encoding (0x01 = Latin1/UTF-8 - the JSON here is plain ASCII) followed
	// by the raw bytes. Confirmed against Feishin's own existing entries in
	// this same database with a real LevelDB reader, not assumed.
	value := append([]byte{0x01}, body...)

	if err := db.Put(key, value, nil); err != nil {
		return fmt.Errorf("writing playlist order: %w", err)
	}
	return nil
}

// chromiumLocalStorageKey builds the on-disk LevelDB key Chromium uses for
// one localStorage entry: "_" + origin + NUL + 0x01 + the actual key -
// reverse-engineered by reading Feishin's own existing entries directly out
// of its database with a real LevelDB reader, not guessed at.
func chromiumLocalStorageKey(key string) []byte {
	b := []byte("_" + localStorageOrigin)
	b = append(b, 0x00, 0x01)
	b = append(b, key...)
	return b
}

// orderedPlaylistIDs returns ownerUsername's playlist IDs from Navidrome's
// own database, by created_at - oldest first, unless descending is set.
// Read-only, via library.OpenDB - the same WAL/permission-aware opener the
// library index uses, so this behaves the same way whether Navidrome's
// database sits on a NAS, is mid-write, or is owned by a different user.
func orderedPlaylistIDs(ctx context.Context, dbPath, ownerUsername string, descending bool) ([]string, error) {
	db, cleanup, err := library.OpenDB(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	// descending picks between two fixed literals, never interpolates
	// caller-supplied text - safe despite not being a placeholder param,
	// since ORDER BY direction can't be parameterized like a value can.
	direction := "ASC"
	if descending {
		direction = "DESC"
	}
	rows, err := db.QueryContext(ctx, `
		SELECT p.id
		FROM playlist p
		JOIN user u ON p.owner_id = u.id
		WHERE u.user_name = ?
		ORDER BY p.created_at `+direction, ownerUsername)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
