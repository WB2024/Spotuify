// Package config loads Spotuify's runtime configuration from the environment
// (and a local .env file, if present), and can persist edits made in the
// Settings screen back to that .env file.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Config holds everything Spotuify needs to talk to Spotify and to know
// where to read/write local state. It's safe to construct with empty
// ClientID/ClientSecret — Load never fails just because credentials are
// missing, since the Settings screen exists to let the user fill them in
// without editing files by hand. Call Validate before attempting to
// authenticate.
type Config struct {
	ClientID     string
	ClientSecret string
	RedirectPort int

	// ExportDir is where playlist exports (JSON, CSV, cover art) are written.
	ExportDir string

	// DownloadCovers controls whether cover art is fetched during export.
	// Turning it off speeds up large batch exports.
	DownloadCovers bool

	// TokenCachePath is where the OAuth token (incl. refresh token) is
	// persisted between runs so the user isn't asked to log in every time.
	TokenCachePath string

	// EnvPath is where Save writes settings back to. It's the same .env
	// file Load reads from.
	EnvPath string
}

const (
	envClientID     = "Spotify_ClientID"
	envClientSecret = "SpotifySecret"
	envRedirectPort = "SPOTIFY_REDIRECT_PORT"
	envExportDir    = "SPOTUIFY_EXPORT_DIR"
	envDownloadArt  = "SPOTUIFY_DOWNLOAD_COVERS"
)

const defaultRedirectPort = 8080

// Load reads .env (if present) and environment variables into a Config.
// A missing .env file, or missing credentials, is not an error — the
// Settings screen is where those get filled in. Real environment variables
// always take precedence over what's in .env.
func Load() (*Config, error) {
	envPath, err := filepath.Abs(".env")
	if err != nil {
		envPath = ".env"
	}
	_ = godotenv.Load(envPath) // ignore error: .env is optional, real env vars still work

	port := defaultRedirectPort
	if raw := os.Getenv(envRedirectPort); raw != "" {
		if p, err := strconv.Atoi(raw); err == nil && p > 0 && p < 65536 {
			port = p
		}
	}

	exportDir := os.Getenv(envExportDir)
	if exportDir == "" {
		exportDir = "exports"
	}

	downloadCovers := true
	if raw := os.Getenv(envDownloadArt); raw != "" {
		if b, err := strconv.ParseBool(raw); err == nil {
			downloadCovers = b
		}
	}

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = "."
	}
	tokenPath := filepath.Join(cacheDir, "spotuify", "token.json")

	return &Config{
		ClientID:       os.Getenv(envClientID),
		ClientSecret:   os.Getenv(envClientSecret),
		RedirectPort:   port,
		ExportDir:      exportDir,
		DownloadCovers: downloadCovers,
		TokenCachePath: tokenPath,
		EnvPath:        envPath,
	}, nil
}

// Validate reports whether the config has what it needs to authenticate
// with Spotify.
func (c *Config) Validate() error {
	if c.ClientID == "" || c.ClientSecret == "" {
		return fmt.Errorf("Spotify credentials are not set — open Settings and fill in Client ID / Client Secret")
	}
	return nil
}

// RedirectURI is the loopback URI Spotify redirects the user's browser back
// to after they approve the app. Spotify requires this to match, byte for
// byte, an entry registered in the app's dashboard settings, and (per
// Spotify's 2023 redirect URI rules for loopback addresses) requires the
// literal IP "127.0.0.1" rather than "localhost".
func (c *Config) RedirectURI() string {
	return fmt.Sprintf("http://127.0.0.1:%d/callback", c.RedirectPort)
}

// Save writes the current config values back to c.EnvPath, preserving any
// lines it doesn't manage (comments, blank lines, unrelated variables) and
// updating/appending the ones it does.
func (c *Config) Save() error {
	values := []envKV{
		{envClientID, c.ClientID},
		{envClientSecret, c.ClientSecret},
		{envRedirectPort, strconv.Itoa(c.RedirectPort)},
		{envExportDir, c.ExportDir},
		{envDownloadArt, strconv.FormatBool(c.DownloadCovers)},
	}
	return upsertEnvFile(c.EnvPath, values)
}

type envKV struct {
	Key   string
	Value string
}

// upsertEnvFile rewrites path, replacing the value of any line whose key
// matches one in values, appending keys that weren't already present, and
// leaving every other line (comments, blank lines, unrelated vars) intact.
func upsertEnvFile(path string, values []envKV) error {
	var lines []string
	if existing, err := os.ReadFile(path); err == nil {
		content := strings.TrimRight(strings.ReplaceAll(string(existing), "\r\n", "\n"), "\n")
		if content != "" {
			lines = strings.Split(content, "\n")
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	seen := make(map[string]bool, len(values))
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, _, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		for _, kv := range values {
			if key == kv.Key {
				lines[i] = kv.Key + "=" + kv.Value
				seen[kv.Key] = true
				break
			}
		}
	}

	for _, kv := range values {
		if !seen[kv.Key] {
			lines = append(lines, kv.Key+"="+kv.Value)
		}
	}

	out := strings.Join(lines, "\n") + "\n"

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(out), 0o600)
}
