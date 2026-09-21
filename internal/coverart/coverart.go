// Package coverart downloads a playlist's highest-resolution cover image,
// shared by both the JSON/CSV export and the M3U8 export so each
// playlist's output folder gets the same "cover.<ext>" treatment.
package coverart

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"spotuify/internal/spotifyapi"
)

// Download fetches the best (largest) available image from images and
// saves it as "cover.<ext>" in dir, returning the path written. It returns
// ("", nil) if images is empty — not an error, just nothing to save.
func Download(ctx context.Context, httpClient *http.Client, images []spotifyapi.Image, dir string) (string, error) {
	img, ok := spotifyapi.BestImage(images)
	if !ok {
		return "", nil
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, img.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %d downloading cover art", resp.StatusCode)
	}

	path := filepath.Join(dir, "cover"+extFromContentType(resp.Header.Get("Content-Type")))
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return "", err
	}
	return path, nil
}

func extFromContentType(ct string) string {
	switch {
	case strings.Contains(ct, "png"):
		return ".png"
	case strings.Contains(ct, "gif"):
		return ".gif"
	case strings.Contains(ct, "webp"):
		return ".webp"
	default:
		return ".jpg" // Spotify's image CDN serves JPEG in the overwhelming majority of cases
	}
}
