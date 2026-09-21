// Package auth handles Spotify's OAuth 2.0 Authorization Code flow for a
// local, installed application: it opens the user's browser, catches the
// redirect on a loopback HTTP server, exchanges the code for tokens, and
// caches the result on disk so future runs can refresh silently.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"golang.org/x/oauth2"

	"spotuify/internal/config"
)

// Scopes requested from the user. Read-only and limited to what's needed to
// enumerate and fully export playlists (including private/collaborative
// ones the user owns or follows).
var Scopes = []string{
	"playlist-read-private",
	"playlist-read-collaborative",
}

var Endpoint = oauth2.Endpoint{
	AuthURL:  "https://accounts.spotify.com/authorize",
	TokenURL: "https://accounts.spotify.com/api/token",
}

// GetClient returns an *http.Client that automatically attaches a valid
// Spotify access token to every request, transparently refreshing (and
// persisting) it as needed. If no cached token exists, it runs the
// interactive browser login first.
func GetClient(ctx context.Context, cfg *config.Config) (*http.Client, error) {
	oauthCfg := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoint:     Endpoint,
		RedirectURL:  cfg.RedirectURI(),
		Scopes:       Scopes,
	}

	tok, err := loadToken(cfg.TokenCachePath)
	if err != nil || tok == nil {
		tok, err = login(ctx, oauthCfg, cfg.RedirectPort)
		if err != nil {
			return nil, err
		}
		if err := saveToken(cfg.TokenCachePath, tok); err != nil {
			return nil, fmt.Errorf("saving token cache: %w", err)
		}
	}

	src := &persistingTokenSource{
		wrapped: oauthCfg.TokenSource(ctx, tok),
		path:    cfg.TokenCachePath,
		last:    tok.AccessToken,
	}

	// Force a refresh check now, and persist immediately if it rotated,
	// rather than waiting for the first API call.
	if _, err := src.Token(); err != nil {
		return nil, fmt.Errorf("refreshing Spotify token: %w", err)
	}

	return oauth2.NewClient(ctx, src), nil
}

// persistingTokenSource wraps an oauth2.TokenSource and writes the token to
// disk any time it changes (i.e. any time the underlying source refreshes
// it), so the next run can reuse it without another browser login.
type persistingTokenSource struct {
	wrapped oauth2.TokenSource
	path    string
	last    string
}

func (s *persistingTokenSource) Token() (*oauth2.Token, error) {
	tok, err := s.wrapped.Token()
	if err != nil {
		return nil, err
	}
	if tok.AccessToken != s.last {
		s.last = tok.AccessToken
		if err := saveToken(s.path, tok); err != nil {
			// Non-fatal: we still have a usable token in memory for this run.
			fmt.Fprintf(os.Stderr, "warning: could not cache refreshed token: %v\n", err)
		}
	}
	return tok, nil
}

// login runs the interactive browser-based Authorization Code flow.
func login(ctx context.Context, oauthCfg *oauth2.Config, port int) (*oauth2.Token, error) {
	state, err := randomState()
	if err != nil {
		return nil, err
	}

	type result struct {
		code string
		err  error
	}
	resultCh := make(chan result, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if errParam := q.Get("error"); errParam != "" {
			resultCh <- result{err: fmt.Errorf("spotify authorization denied: %s", errParam)}
			fmt.Fprint(w, "Authorization failed. You can close this tab and return to Spotuify.")
			return
		}
		if q.Get("state") != state {
			resultCh <- result{err: fmt.Errorf("state mismatch in OAuth callback (possible CSRF)")}
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}
		code := q.Get("code")
		if code == "" {
			resultCh <- result{err: fmt.Errorf("no authorization code in callback")}
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}
		resultCh <- result{code: code}
		fmt.Fprint(w, "Spotuify is authorized. You can close this tab and return to your terminal.")
	})

	srv := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", port), Handler: mux}
	ln, err := listen(srv.Addr)
	if err != nil {
		return nil, fmt.Errorf(
			"could not start local callback server on %s (is another process using this port? "+
				"set SPOTIFY_REDIRECT_PORT to change it and update your Spotify app's Redirect URI to match): %w",
			srv.Addr, err,
		)
	}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Shutdown(context.Background())

	authURL := oauthCfg.AuthCodeURL(state, oauth2.AccessTypeOffline)

	fmt.Println("Opening your browser to log in to Spotify...")
	fmt.Println("If it doesn't open automatically, visit this URL:")
	fmt.Println(authURL)
	_ = openBrowser(authURL)

	select {
	case res := <-resultCh:
		if res.err != nil {
			return nil, res.err
		}
		tok, err := oauthCfg.Exchange(ctx, res.code)
		if err != nil {
			return nil, fmt.Errorf("exchanging authorization code: %w", err)
		}
		return tok, nil
	case <-time.After(3 * time.Minute):
		return nil, fmt.Errorf("timed out waiting for Spotify login in the browser")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

func loadToken(path string) (*oauth2.Token, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var tok oauth2.Token
	if err := json.Unmarshal(b, &tok); err != nil {
		return nil, err
	}
	return &tok, nil
}

func saveToken(path string, tok *oauth2.Token) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
