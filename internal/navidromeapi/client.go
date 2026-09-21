// Package navidromeapi is a small client for Navidrome's own REST API
// (distinct from both the Subsonic-compatible API and the read-only
// database access in internal/library). It handles the two things a
// scanned-in .m3u8 can't carry on its own: a playlist's cover art (POST
// .../image) and its description (Navidrome's "comment" field — .m3u8/
// EXTM3U playlists have no way to set this from the file itself; only its
// own .nsp Smart Playlist format supports an in-file comment).
package navidromeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Client is a Navidrome REST API client. It logs in lazily on first use
// and reuses the resulting token for the rest of its lifetime — construct
// one per run, not one per call.
type Client struct {
	baseURL    string
	username   string
	password   string
	httpClient *http.Client

	mu    sync.Mutex
	token string
}

// New builds a client for a Navidrome server at baseURL (e.g.
// "http://navidrome.example.com", no trailing slash required).
func New(baseURL, username, password string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		username:   username,
		password:   password,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token string `json:"token"`
}

func (c *Client) authToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" {
		return c.token, nil
	}

	body, err := json.Marshal(loginRequest{Username: c.username, Password: c.password})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/auth/login", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("logging into Navidrome: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Navidrome login failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var out loginResponse
	if err := json.Unmarshal(respBody, &out); err != nil || out.Token == "" {
		return "", fmt.Errorf("Navidrome login response didn't include a token: %s", strings.TrimSpace(string(respBody)))
	}

	c.token = out.Token
	return c.token, nil
}

// UploadPlaylistImage sets a Navidrome playlist's cover art. playlistID is
// Navidrome's own internal ID for the playlist (not the Spotify one) —
// see library.LookupPlaylistIDByPath for how to find it. Matches the
// multipart shape Navidrome's own web UI sends: a single "image" field,
// content-type application/octet-stream (multipart.CreateFormFile's
// default).
func (c *Client) UploadPlaylistImage(ctx context.Context, playlistID string, imageData []byte) error {
	token, err := c.authToken(ctx)
	if err != nil {
		return err
	}

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("image", "image")
	if err != nil {
		return err
	}
	if _, err := part.Write(imageData); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}

	url := fmt.Sprintf("%s/api/playlist/%s/image", c.baseURL, playlistID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("x-nd-authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("uploading playlist cover art: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return fmt.Errorf("Navidrome rejected the cover art upload (%d): %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}

// getPlaylist fetches a playlist's full JSON representation, as a generic
// map rather than a fixed struct, so UpdatePlaylistComment can round-trip
// every field Navidrome already has for it — a PUT that only sent
// {"comment": ...} would risk Navidrome treating that as the playlist's
// complete new state and clearing everything else (name, public, rules...).
func (c *Client) getPlaylist(ctx context.Context, playlistID string) (map[string]any, error) {
	token, err := c.authToken(ctx)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/api/playlist/%s", c.baseURL, playlistID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-nd-authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching playlist from Navidrome: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Navidrome rejected fetching the playlist (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decoding playlist response: %w", err)
	}
	return out, nil
}

// UpdatePlaylistComment sets a Navidrome playlist's comment/description
// field — shown as the playlist's description in Navidrome/Feishin's UI —
// preserving every other field Navidrome already has for it (see
// getPlaylist).
func (c *Client) UpdatePlaylistComment(ctx context.Context, playlistID, comment string) error {
	token, err := c.authToken(ctx)
	if err != nil {
		return err
	}

	current, err := c.getPlaylist(ctx, playlistID)
	if err != nil {
		return err
	}
	current["comment"] = comment

	body, err := json.Marshal(current)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/api/playlist/%s", c.baseURL, playlistID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-nd-authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("updating playlist description: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return fmt.Errorf("Navidrome rejected the description update (%d): %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}
