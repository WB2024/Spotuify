package tui

import (
	"context"
	"fmt"
	"os"
	"time"

	"spotuify/internal/config"
	"spotuify/internal/library"
	"spotuify/internal/navidromeapi"
)

// uploadCoverArt sets a just-written playlist's cover art in Navidrome
// itself, via its authenticated REST API — Navidrome doesn't pick up a
// cover.jpg file sitting in the playlist's folder the way it auto-imports
// the .m3u8 (verified directly against the database: playlists with a
// folder-adjacent cover.jpg but no explicit upload have an empty
// uploaded_image column). Since Navidrome needs to have scanned the new
// (or changed) .m3u8 in before a playlist row — and therefore an ID to
// upload against — exists, this polls briefly for that row to appear.
// Failure here is never fatal to the playlist: the .m3u8 and local
// cover.jpg are already written regardless.
func uploadCoverArt(ctx context.Context, ndClient *navidromeapi.Client, cfg *config.Config, m3u8Path, coverPath string, status func(string)) error {
	const (
		attempts = 15
		interval = 2 * time.Second
	)

	var playlistID string
	for i := 1; i <= attempts; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		id, found, err := library.LookupPlaylistIDByPath(ctx, cfg.NavidromeDBPath, cfg.NavidromeMusicPath, m3u8Path)
		if err != nil {
			return fmt.Errorf("looking up playlist in Navidrome: %w", err)
		}
		if found {
			playlistID = id
			break
		}
		status(fmt.Sprintf("waiting for Navidrome to scan the new playlist in... (%d/%d)", i, attempts))
		t := time.NewTimer(interval)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		}
	}
	if playlistID == "" {
		return fmt.Errorf("Navidrome hadn't scanned the playlist in after %s — cover art not uploaded (it'll show once Navidrome's next scan picks up the file)", time.Duration(attempts)*interval)
	}

	imageData, err := os.ReadFile(coverPath)
	if err != nil {
		return fmt.Errorf("reading cover art file: %w", err)
	}

	return ndClient.UploadPlaylistImage(ctx, playlistID, imageData)
}
