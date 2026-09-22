// Package lidarrapi is a small client for the parts of Lidarr's v1 API
// Spotuify needs: looking up an album (by name, or exactly by MusicBrainz
// release-group ID), adding one, flipping monitoring on one that's already
// in the library, and asking Lidarr to go search for it.
//
// Adding is done the way Lidarr's own "Add New" UI does it for a single
// album — POST /api/v1/album with the album resource straight from lookup —
// which adds the artist (unmonitored, no other albums) if Lidarr doesn't
// have them yet and monitors just that album, rather than pulling in an
// artist's whole discography.
package lidarrapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// New builds a client for a Lidarr server at baseURL (e.g.
// "http://192.168.1.10:8686"; a trailing slash is fine).
func New(baseURL, apiKey string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

type SystemStatus struct {
	AppName string `json:"appName"`
	Version string `json:"version"`
}

// Status fetches Lidarr's system status — the cheapest way to check the
// URL and API key actually work.
func (c *Client) Status(ctx context.Context) (*SystemStatus, error) {
	var out SystemStatus
	if err := c.get(ctx, "/api/v1/system/status", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type RootFolder struct {
	ID                       int    `json:"id"`
	Name                     string `json:"name"`
	Path                     string `json:"path"`
	DefaultQualityProfileID  int    `json:"defaultQualityProfileId"`
	DefaultMetadataProfileID int    `json:"defaultMetadataProfileId"`
}

func (c *Client) RootFolders(ctx context.Context) ([]RootFolder, error) {
	var out []RootFolder
	if err := c.get(ctx, "/api/v1/rootfolder", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

type Profile struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func (c *Client) QualityProfiles(ctx context.Context) ([]Profile, error) {
	var out []Profile
	if err := c.get(ctx, "/api/v1/qualityprofile", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) MetadataProfiles(ctx context.Context) ([]Profile, error) {
	var out []Profile
	if err := c.get(ctx, "/api/v1/metadataprofile", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Artist is the subset of Lidarr's artist resource Spotuify reads. ID is 0
// for an artist Lidarr doesn't have yet.
type Artist struct {
	ID                int    `json:"id"`
	ArtistName        string `json:"artistName"`
	ForeignArtistID   string `json:"foreignArtistId"`
	Monitored         bool   `json:"monitored"`
	QualityProfileID  int    `json:"qualityProfileId"`
	MetadataProfileID int    `json:"metadataProfileId"`
}

// Album is the subset of Lidarr's album resource Spotuify reads, plus the
// complete resource as returned (raw) so it can be POSTed back verbatim —
// the same thing Lidarr's UI does — without modelling every field. ID is
// 0 for an album Lidarr doesn't have yet.
type Album struct {
	ID             int    `json:"id"`
	Title          string `json:"title"`
	ForeignAlbumID string `json:"foreignAlbumId"`
	AlbumType      string `json:"albumType"`
	ReleaseDate    string `json:"releaseDate"`
	Disambiguation string `json:"disambiguation"`
	Monitored      bool   `json:"monitored"`
	Artist         Artist `json:"artist"`

	raw map[string]any
}

func (a *Album) UnmarshalJSON(b []byte) error {
	type plain Album
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*a = Album(p)
	a.raw = map[string]any{}
	return json.Unmarshal(b, &a.raw)
}

// InLidarr reports whether Lidarr already has this album in its library
// (monitored or not).
func (a Album) InLidarr() bool { return a.ID != 0 }

// Year is the release year, or "" if unknown.
func (a Album) Year() string {
	if len(a.ReleaseDate) >= 4 && a.ReleaseDate[:4] != "0001" {
		return a.ReleaseDate[:4]
	}
	return ""
}

// LookupAlbums searches Lidarr's metadata source for albums matching term:
// free text ("Artist Album"), or exactly one album by MusicBrainz
// release-group ID with "lidarr:<mbid>". Results that Lidarr already has
// come back with their library ID and monitored state filled in.
func (c *Client) LookupAlbums(ctx context.Context, term string) ([]Album, error) {
	var out []Album
	if err := c.get(ctx, "/api/v1/album/lookup", url.Values{"term": {term}}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// LookupAlbumByMBID looks up exactly one album by MusicBrainz release-group
// ID. Returns (nil, nil) if Lidarr's metadata source doesn't know it.
func (c *Client) LookupAlbumByMBID(ctx context.Context, releaseGroupID string) (*Album, error) {
	albums, err := c.LookupAlbums(ctx, "lidarr:"+releaseGroupID)
	if err != nil {
		return nil, err
	}
	for i := range albums {
		if albums[i].ForeignAlbumID == releaseGroupID {
			return &albums[i], nil
		}
	}
	return nil, nil
}

// AddOptions is what Lidarr needs to add an album whose artist it doesn't
// have yet (root folder + profiles), plus whether to search immediately.
type AddOptions struct {
	RootFolderPath    string
	QualityProfileID  int
	MetadataProfileID int
	SearchForNewAlbum bool
}

// AddAlbum adds an album from a lookup result to Lidarr, monitored. If the
// artist isn't in Lidarr yet they're added too — unmonitored for anything
// else, with "monitor new items" off, so only this album is wanted.
func (c *Client) AddAlbum(ctx context.Context, album Album, opts AddOptions) (*Album, error) {
	if album.raw == nil {
		return nil, fmt.Errorf("album %q wasn't obtained from a lookup", album.Title)
	}
	body := cloneMap(album.raw)
	body["monitored"] = true
	body["addOptions"] = map[string]any{"searchForNewAlbum": opts.SearchForNewAlbum}

	artist, _ := body["artist"].(map[string]any)
	artist = cloneMap(artist)
	artist["monitored"] = true
	if album.Artist.ID == 0 {
		artist["rootFolderPath"] = opts.RootFolderPath
		artist["qualityProfileId"] = opts.QualityProfileID
		artist["metadataProfileId"] = opts.MetadataProfileID
		artist["monitorNewItems"] = "none"
		// Deliberately no artist addOptions: Lidarr's AddArtistService
		// forces the artist unmonitored when addOptions.monitor is "none"
		// (verified in its source), and an unmonitored artist is skipped by
		// RSS sync — which would quietly defeat "add only". With no options
		// nothing of theirs gets bulk-monitored and no artist-level search
		// is queued; monitorNewItems "none" keeps albums discovered on
		// refresh unmonitored, and the album itself is sent monitored.
		delete(artist, "addOptions")
	}
	body["artist"] = artist

	var out Album
	if err := c.post(ctx, "/api/v1/album", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// MonitorAlbums sets the monitored flag on albums already in Lidarr.
func (c *Client) MonitorAlbums(ctx context.Context, albumIDs []int, monitored bool) error {
	body := map[string]any{"albumIds": albumIDs, "monitored": monitored}
	return c.put(ctx, "/api/v1/album/monitor", body)
}

// SearchAlbums queues Lidarr's own "search for this album" command.
func (c *Client) SearchAlbums(ctx context.Context, albumIDs []int) error {
	body := map[string]any{"name": "AlbumSearch", "albumIds": albumIDs}
	return c.post(ctx, "/api/v1/command", body, nil)
}

func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

func (c *Client) post(ctx context.Context, path string, body any, out any) error {
	return c.send(ctx, http.MethodPost, path, body, out)
}

func (c *Client) put(ctx context.Context, path string, body any) error {
	return c.send(ctx, http.MethodPut, path, body, nil)
}

func (c *Client) send(ctx context.Context, method, path string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	req.Header.Set("X-Api-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("talking to Lidarr: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("reading Lidarr response: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("Lidarr rejected the API key (401)")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("Lidarr returned %d: %s", resp.StatusCode, errorDetail(data))
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decoding Lidarr response: %w", err)
	}
	return nil
}

// errorDetail pulls the human-readable part out of Lidarr's error bodies:
// validation failures come back as a list of {propertyName, errorMessage},
// most other errors as {message}.
func errorDetail(data []byte) string {
	var validation []struct {
		PropertyName string `json:"propertyName"`
		ErrorMessage string `json:"errorMessage"`
	}
	if err := json.Unmarshal(data, &validation); err == nil && len(validation) > 0 {
		parts := make([]string, 0, len(validation))
		for _, v := range validation {
			parts = append(parts, strings.TrimSpace(v.PropertyName+": "+v.ErrorMessage))
		}
		return strings.Join(parts, "; ")
	}
	var obj struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(data, &obj); err == nil && obj.Message != "" {
		return obj.Message
	}
	s := strings.TrimSpace(string(data))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+6)
	for k, v := range m {
		out[k] = v
	}
	return out
}
