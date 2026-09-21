package spotifyapi

import (
	"context"
	"fmt"
	"net/url"
)

const pageLimit = 50 // Spotify's max page size for these endpoints

// CurrentUser fetches the profile of the authenticated user (GET /me).
func (c *Client) CurrentUser(ctx context.Context) (*User, error) {
	var u User
	if err := c.get(ctx, "/me", &u); err != nil {
		return nil, fmt.Errorf("fetching current user: %w", err)
	}
	return &u, nil
}

// UserPlaylists returns every playlist owned or followed by the current
// user, paging through the full result set.
func (c *Client) UserPlaylists(ctx context.Context) ([]SimplifiedPlaylist, error) {
	var all []SimplifiedPlaylist
	offset := 0
	for {
		var page Paging[SimplifiedPlaylist]
		path := fmt.Sprintf("/me/playlists?limit=%d&offset=%d", pageLimit, offset)
		if err := c.get(ctx, path, &page); err != nil {
			return nil, fmt.Errorf("listing playlists (offset %d): %w", offset, err)
		}
		all = append(all, page.Items...)
		if page.Next == "" || len(page.Items) == 0 {
			break
		}
		offset += pageLimit
	}
	return all, nil
}

// Playlist fetches full details (including follower count) for a single
// playlist by ID.
func (c *Client) Playlist(ctx context.Context, id string) (*FullPlaylist, error) {
	var p FullPlaylist
	path := fmt.Sprintf("/playlists/%s", url.PathEscape(id))
	if err := c.get(ctx, path, &p); err != nil {
		return nil, fmt.Errorf("fetching playlist %s: %w", id, err)
	}
	return &p, nil
}

// PlaylistTracks returns every track in a playlist, in playlist order,
// paging through the full result set. Progress, if non-nil, is called after
// each page with the number of items fetched so far and the total reported
// by Spotify.
//
// This uses /playlists/{id}/items rather than the older /playlists/{id}/tracks
// path: Spotify has renamed the endpoint, and the old path now returns 403.
func (c *Client) PlaylistTracks(ctx context.Context, id string, progress func(fetched, total int)) ([]PlaylistTrackItem, error) {
	var all []PlaylistTrackItem
	offset := 0
	for {
		var page Paging[PlaylistTrackItem]
		path := fmt.Sprintf("/playlists/%s/items?limit=%d&offset=%d", url.PathEscape(id), pageLimit, offset)
		if err := c.get(ctx, path, &page); err != nil {
			return nil, fmt.Errorf("fetching tracks for playlist %s (offset %d): %w", id, offset, err)
		}
		all = append(all, page.Items...)
		if progress != nil {
			progress(len(all), page.Total)
		}
		if page.Next == "" || len(page.Items) == 0 {
			break
		}
		offset += pageLimit
	}
	return all, nil
}
