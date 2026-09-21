package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"spotuify/internal/config"
	"spotuify/internal/library"
	"spotuify/internal/navidromeapi"
)

// syncNavidromeMetadata fills in the two things a scanned-in .m3u8 can't
// carry on its own: cover art (Navidrome doesn't pick up a cover.jpg file
// sitting in the playlist's folder — verified directly against the
// database: a playlist with one there but no explicit upload still has an
// empty uploaded_image column) and the description (Navidrome's "comment"
// field; .m3u8/EXTM3U gives it no way to set this from the file, unlike
// Navidrome's own .nsp Smart Playlist format). Since Navidrome needs to
// have scanned the new (or changed) .m3u8 in before a playlist row — and
// therefore an ID to act on — exists, this polls briefly for that row.
// Every step here is best-effort: failure never invalidates the playlist
// itself, which is already fully written (.m3u8, missing.txt, cover.jpg)
// regardless of what happens in this function.
func syncNavidromeMetadata(ctx context.Context, ndClient *navidromeapi.Client, cfg *config.Config, m3u8Path, coverPath, description string, status func(string)) error {
	playlistID, err := waitForNavidromePlaylist(ctx, cfg, m3u8Path, status)
	if err != nil {
		return err
	}

	var problems []string

	if coverPath != "" {
		imageData, err := os.ReadFile(coverPath)
		if err != nil {
			problems = append(problems, "cover art: "+err.Error())
		} else if err := ndClient.UploadPlaylistImage(ctx, playlistID, imageData); err != nil {
			problems = append(problems, "cover art: "+err.Error())
		}
	}

	if description != "" {
		if err := ndClient.UpdatePlaylistComment(ctx, playlistID, description); err != nil {
			problems = append(problems, "description: "+err.Error())
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

func waitForNavidromePlaylist(ctx context.Context, cfg *config.Config, m3u8Path string, status func(string)) (string, error) {
	const (
		attempts = 15
		interval = 2 * time.Second
	)

	for i := 1; i <= attempts; i++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		id, found, err := library.LookupPlaylistIDByPath(ctx, cfg.NavidromeDBPath, cfg.NavidromeMusicPath, m3u8Path)
		if err != nil {
			return "", fmt.Errorf("looking up playlist in Navidrome: %w", err)
		}
		if found {
			return id, nil
		}
		status(fmt.Sprintf("waiting for Navidrome to scan the new playlist in... (%d/%d)", i, attempts))
		t := time.NewTimer(interval)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return "", ctx.Err()
		}
	}
	return "", fmt.Errorf("Navidrome hadn't scanned the playlist in after %s", time.Duration(attempts)*interval)
}
