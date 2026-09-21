package spotifyapi

import "encoding/json"

// Paging is Spotify's generic pagination envelope, used by both the
// playlists list and the playlist items (tracks) list.
type Paging[T any] struct {
	Href     string `json:"href"`
	Limit    int    `json:"limit"`
	Next     string `json:"next"`
	Offset   int    `json:"offset"`
	Previous string `json:"previous"`
	Total    int    `json:"total"`
	Items    []T    `json:"items"`
}

type Image struct {
	URL    string `json:"url"`
	Height int    `json:"height"`
	Width  int    `json:"width"`
}

type ExternalURLs struct {
	Spotify string `json:"spotify"`
}

type User struct {
	ID           string       `json:"id"`
	DisplayName  string       `json:"display_name"`
	URI          string       `json:"uri"`
	ExternalURLs ExternalURLs `json:"external_urls"`
}

type Followers struct {
	Total int `json:"total"`
}

// PlaylistItemsRef is the summary (href + count) Spotify embeds in a
// playlist object for its track listing. Note: Spotify's field for this is
// named "items", not "tracks" — it was renamed alongside the
// /playlists/{id}/tracks -> /playlists/{id}/items endpoint migration.
type PlaylistItemsRef struct {
	Href  string `json:"href"`
	Total int    `json:"total"`
}

// SimplifiedPlaylist is what /me/playlists returns for each playlist.
type SimplifiedPlaylist struct {
	ID            string           `json:"id"`
	Name          string           `json:"name"`
	Description   string           `json:"description"`
	Public        *bool            `json:"public"`
	Collaborative bool             `json:"collaborative"`
	Owner         User             `json:"owner"`
	Images        []Image          `json:"images"`
	SnapshotID    string           `json:"snapshot_id"`
	URI           string           `json:"uri"`
	ExternalURLs  ExternalURLs     `json:"external_urls"`
	Tracks        PlaylistItemsRef `json:"items"`
}

// FullPlaylist is what GET /playlists/{id} returns: everything in
// SimplifiedPlaylist plus follower counts. Its embedded Tracks paging
// object only holds the first page, so track fetching is done separately
// and paged to completion.
type FullPlaylist struct {
	SimplifiedPlaylist
	Followers Followers `json:"followers"`
}

type Artist struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	URI          string       `json:"uri"`
	ExternalURLs ExternalURLs `json:"external_urls"`
}

type Album struct {
	ID                   string       `json:"id"`
	Name                 string       `json:"name"`
	AlbumType            string       `json:"album_type"`
	ReleaseDate          string       `json:"release_date"`
	ReleaseDatePrecision string       `json:"release_date_precision"`
	TotalTracks          int          `json:"total_tracks"`
	Images               []Image      `json:"images"`
	Artists              []Artist     `json:"artists"`
	URI                  string       `json:"uri"`
	ExternalURLs         ExternalURLs `json:"external_urls"`
}

type ExternalIDs struct {
	ISRC string `json:"isrc"`
	EAN  string `json:"ean"`
	UPC  string `json:"upc"`
}

// Track covers both regular tracks and the subset of fields present on
// local files added to a playlist (IsLocal true; most Spotify-catalog
// fields will be zero for those).
type Track struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	URI          string       `json:"uri"`
	DurationMs   int          `json:"duration_ms"`
	Explicit     bool         `json:"explicit"`
	Popularity   int          `json:"popularity"`
	PreviewURL   string       `json:"preview_url"`
	TrackNumber  int          `json:"track_number"`
	DiscNumber   int          `json:"disc_number"`
	IsLocal      bool         `json:"is_local"`
	Album        Album        `json:"album"`
	Artists      []Artist     `json:"artists"`
	ExternalIDs  ExternalIDs  `json:"external_ids"`
	ExternalURLs ExternalURLs `json:"external_urls"`
}

// PlaylistTrackItem is one row of a playlist's track listing: the track
// itself plus playlist-specific metadata (when/who added it).
//
// Spotify's field for the track object here is named "item" on the current
// /playlists/{id}/items endpoint; older documentation (and possibly older
// API versions) call it "track". UnmarshalJSON accepts either, so decoding
// Spotify's responses keeps working if they change it back or serve both.
// MarshalJSON always writes our own export's field as "track" — that's
// Spotuify's export schema, independent of what Spotify happens to call it.
type PlaylistTrackItem struct {
	AddedAt string `json:"added_at"`
	AddedBy User   `json:"added_by"`
	IsLocal bool   `json:"is_local"`
	Track   *Track `json:"-"`
}

func (p *PlaylistTrackItem) UnmarshalJSON(data []byte) error {
	type alias PlaylistTrackItem // avoid recursing back into this method
	aux := struct {
		*alias
		Item      *Track `json:"item"`
		TrackJSON *Track `json:"track"`
	}{alias: (*alias)(p)}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if aux.Item != nil {
		p.Track = aux.Item
	} else {
		p.Track = aux.TrackJSON
	}
	return nil
}

func (p PlaylistTrackItem) MarshalJSON() ([]byte, error) {
	type alias PlaylistTrackItem
	return json.Marshal(struct {
		alias
		Track *Track `json:"track"`
	}{alias: alias(p), Track: p.Track})
}

// BestImage returns the highest-resolution image (by pixel area) from a
// Spotify image list, or false if there are none. Spotify returns images
// largest-first by convention, but we don't rely on that.
func BestImage(images []Image) (Image, bool) {
	var best Image
	found := false
	bestArea := -1
	for _, img := range images {
		area := img.Height * img.Width
		if !found || area > bestArea {
			best = img
			bestArea = area
			found = true
		}
	}
	return best, found
}
