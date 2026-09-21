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

	// NavidromeDBPath is the path to Navidrome's navidrome.db on this
	// machine — the source of truth for the local library (Navidrome has
	// already scanned and tagged everything, MusicBrainz IDs included, so
	// Spotuify reads its database rather than re-scanning files itself).
	NavidromeDBPath string

	// NavidromeMusicPath is this machine's path to the same music folder
	// Navidrome mounts as its library root (its ND_MUSICFOLDER). Navidrome
	// stores each track's path relative to that root — the same
	// remote-path-mapping idea *arr apps use — so a local track's real
	// path is filepath.Join(NavidromeMusicPath, <path from the database>).
	NavidromeMusicPath string

	// M3U8Dir is where generated .m3u8 playlists (and their accompanying
	// missing-tracks reports and cover art) are written, one subfolder per
	// playlist.
	M3U8Dir string

	// ResolveMusicBrainzISRC controls whether, during matching, a Spotify
	// track that didn't already match by tag gets bridged via the
	// MusicBrainz API (looking up which recording(s) its ISRC belongs to,
	// then checking those against local files' embedded MusicBrainz
	// Recording IDs). This only runs for tracks that still need it after
	// the free tag-based match, so its cost is bounded by how much of the
	// current playlist(s) is unmatched, not the size of the whole library.
	// It's rate-limited to the MusicBrainz API's documented 1
	// request/second and cached indefinitely, so a given ISRC only ever
	// costs time once. Turning it off relies on direct ISRC tags (as
	// Navidrome extracted them) and fuzzy matching only.
	ResolveMusicBrainzISRC bool

	// EnableFuzzyMatching controls whether tracks with no ISRC match (no
	// tag, no resolvable MusicBrainz ID) fall back to normalized
	// artist/title text similarity against the local library.
	EnableFuzzyMatching bool

	// TokenCachePath is where the OAuth token (incl. refresh token) is
	// persisted between runs so the user isn't asked to log in every time.
	TokenCachePath string

	// LibraryCachePath is where resolved ISRC-to-MusicBrainz-recording
	// lookups are cached between runs (see ResolveMusicBrainzISRC).
	LibraryCachePath string

	// EnvPath is where Save writes settings back to. It's the same .env
	// file Load reads from.
	EnvPath string
}

const (
	envClientID       = "Spotify_ClientID"
	envClientSecret   = "SpotifySecret"
	envRedirectPort   = "SPOTIFY_REDIRECT_PORT"
	envExportDir      = "SPOTUIFY_EXPORT_DIR"
	envDownloadArt    = "SPOTUIFY_DOWNLOAD_COVERS"
	envNavidromeDB    = "SPOTUIFY_NAVIDROME_DB"
	envNavidromeMusic = "SPOTUIFY_NAVIDROME_MUSIC_PATH"
	envM3U8Dir        = "SPOTUIFY_M3U8_DIR"
	envResolveMBISRC  = "SPOTUIFY_RESOLVE_MUSICBRAINZ_ISRC"
	envEnableFuzzy    = "SPOTUIFY_FUZZY_MATCH"
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

	downloadCovers := parseBoolDefault(os.Getenv(envDownloadArt), true)
	resolveMBISRC := parseBoolDefault(os.Getenv(envResolveMBISRC), true)
	enableFuzzy := parseBoolDefault(os.Getenv(envEnableFuzzy), true)

	m3u8Dir := os.Getenv(envM3U8Dir)
	if m3u8Dir == "" {
		m3u8Dir = "playlists"
	}

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = "."
	}
	tokenPath := filepath.Join(cacheDir, "spotuify", "token.json")
	libraryCachePath := filepath.Join(cacheDir, "spotuify", "mb_isrc_cache.json")

	return &Config{
		ClientID:               os.Getenv(envClientID),
		ClientSecret:           os.Getenv(envClientSecret),
		RedirectPort:           port,
		ExportDir:              exportDir,
		DownloadCovers:         downloadCovers,
		NavidromeDBPath:        os.Getenv(envNavidromeDB),
		NavidromeMusicPath:     os.Getenv(envNavidromeMusic),
		M3U8Dir:                m3u8Dir,
		ResolveMusicBrainzISRC: resolveMBISRC,
		EnableFuzzyMatching:    enableFuzzy,
		TokenCachePath:         tokenPath,
		LibraryCachePath:       libraryCachePath,
		EnvPath:                envPath,
	}, nil
}

func parseBoolDefault(raw string, def bool) bool {
	if raw == "" {
		return def
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return def
	}
	return b
}

// Validate reports whether the config has what it needs to authenticate
// with Spotify.
func (c *Config) Validate() error {
	if c.ClientID == "" || c.ClientSecret == "" {
		return fmt.Errorf("Spotify credentials are not set — open Settings and fill in Client ID / Client Secret")
	}
	return nil
}

// ValidateLibrary reports whether the config has what it needs to match
// Spotify tracks against the Navidrome-indexed local library.
func (c *Config) ValidateLibrary() error {
	if c.NavidromeDBPath == "" || c.NavidromeMusicPath == "" {
		return fmt.Errorf("Navidrome isn't configured — open Settings and set the database path and music folder")
	}
	if info, err := os.Stat(c.NavidromeDBPath); err != nil {
		return fmt.Errorf("Navidrome database %q: %w", c.NavidromeDBPath, err)
	} else if info.IsDir() {
		return fmt.Errorf("Navidrome database path %q is a directory, not navidrome.db", c.NavidromeDBPath)
	}
	info, err := os.Stat(c.NavidromeMusicPath)
	if err != nil {
		return fmt.Errorf("Navidrome music path %q: %w", c.NavidromeMusicPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("Navidrome music path %q is not a directory", c.NavidromeMusicPath)
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
		{envNavidromeDB, c.NavidromeDBPath},
		{envNavidromeMusic, c.NavidromeMusicPath},
		{envM3U8Dir, c.M3U8Dir},
		{envResolveMBISRC, strconv.FormatBool(c.ResolveMusicBrainzISRC)},
		{envEnableFuzzy, strconv.FormatBool(c.EnableFuzzyMatching)},
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
