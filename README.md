# Spotuify

A terminal UI for deep-exporting your Spotify playlists — full metadata,
complete tracklists (with added-by/added-at), track & album metadata, and
the highest-resolution cover art available — to JSON and CSV.

Built with [Bubble Tea](https://github.com/charmbracelet/bubbletea).

## Status

First draft. Currently does one thing: browse your playlists and export
them. More features (playback, search, etc.) can be layered on top of this
foundation later.

## Setup

1. **Install Go** (1.22+): https://go.dev/doc/install

2. **Spotify app credentials.** You've already created an app in the
   [Spotify Developer Dashboard](https://developer.spotify.com/dashboard).
   Open its settings and add this exact Redirect URI:

   ```
   http://127.0.0.1:8080/callback
   ```

   (Spotify requires the literal IP `127.0.0.1`, not `localhost`, for
   loopback redirects.) If you'd rather use a different port, set
   `SPOTIFY_REDIRECT_PORT` in `.env` and update the dashboard to match.

3. **`.env` file.** You already have one in the repo root with
   `Spotify_ClientID` and `SpotifySecret` set — it's gitignored, so it won't
   be committed. `.env.example` documents the format if you need to recreate
   it. You can also skip this step entirely and fill credentials in from the
   app's own Settings screen instead (see below).

4. **Fetch dependencies and build:**

   ```bash
   go mod tidy
   go build ./cmd/spotuify
   ```

5. **Run it:**

   ```bash
   ./spotuify
   ```

   You land on a main menu: **Export Playlists** and **Settings**. Picking
   Export Playlists is what triggers Spotify login the first time (opens
   your browser, read-only access to your playlists — see Scopes below).
   After that, a refresh token is cached in your OS user-cache directory
   (e.g. `~/.cache/spotuify/token.json` on Linux) so you won't be asked
   again until it's revoked or you log out from Settings.

## Using it

Every screen shares the same chrome: a gradient logo, bordered panels, and
a context-aware help bar at the bottom (press `?` to expand it into the
full key reference). `ctrl+c` quits from anywhere.

**Main menu:** `↑`/`↓` navigate, `enter` select, `q` quit.

**Export Playlists:**
- Arrow keys / `j`/`k` — move through your playlists; the right-hand panel
  shows full details (owner, visibility, track count, description) for
  whichever playlist is highlighted
- `space` — check a playlist for batch export
- `a` — select/deselect all
- `enter` — export the checked playlists (or just the highlighted one, if
  none are checked) — shows a live status table (one row per playlist,
  updating as each is fetched and written) alongside an overall progress bar
- `/` — filter playlists by name
- `esc` — back to the main menu (cancels an in-progress export)

**Settings:**
- `↑`/`↓` — move between fields
- `enter` — edit the highlighted field (text fields), or activate an action
  row (Log out / Save / Back)
- while editing a field: type normally, `enter`/`esc` to confirm and stop
  editing
- `space` — toggle a checkbox field (e.g. Download cover art)
- `esc` (when not editing) — back to the main menu

Settings lets you set/change your Spotify Client ID and Secret, the export
directory, the OAuth redirect port, and whether cover art is downloaded —
all written back to `.env` on Save. Changing credentials or hitting "Log
out" clears the cached session, so the next export re-triggers browser
login.

## What gets exported

For each playlist, under `exports/<playlist-name>-<playlist-id>/`:

- **`playlist.json`** — the full playlist object (name, description,
  owner, follower count, snapshot ID, etc.) plus every track with its
  playlist-specific data (`added_at`, `added_by`) and full Spotify track/
  album/artist metadata (ISRC, duration, popularity, explicit flag,
  release date, external URLs, and more).
- **`tracks.csv`** — one row per track: position, added date/by, track
  name, artists, album, release date, duration, explicit, popularity,
  ISRC, IDs, URLs.
- **`cover.jpg`** (or `.png`/`.webp`) — the highest-resolution cover art
  Spotify has for that playlist, picked by comparing pixel area across
  every image Spotify returns (skipped if "Download cover art" is off in
  Settings).

`exports/` is gitignored by default.

Note on cover art resolution: for playlists with Spotify's auto-generated
4-track mosaic cover, the API gives multiple sizes (verified up to
640×640) and Spotuify always picks the largest. For a custom cover a user
uploaded, Spotify's API returns exactly one URL with no size metadata —
we verified against the live API that this is genuinely the only size
available (same image, byte-for-byte, from every Spotify image CDN
hostname); there's no larger version to request. So "largest possible" is
already what's downloaded in both cases — the resolution ceiling on
custom covers is on Spotify's end, not ours.

Note: Spotify's playlist-items endpoint currently doesn't return `popularity`
or `preview_url` on tracks (both fields still exist in the CSV/JSON schema,
but will be empty/0 — that's Spotify not sending the data, not an export
bug).

## Rate limits

Spotify doesn't publish a fixed requests/second number — it enforces a
rolling per-app limit and returns `429` with a `Retry-After` header when
you exceed it
(docs: https://developer.spotify.com/documentation/web-api/concepts/rate-limits).
Spotuify self-throttles to a conservative steady rate and, on a `429`,
sleeps for exactly what `Retry-After` says before retrying — so large
libraries export reliably instead of tripping the limit repeatedly.

## A note on API access

As of late 2024, Spotify restricts several endpoints (Audio Features,
Audio Analysis, Recommendations, Related Artists, and others) to apps
that have been granted **Extended Quota Mode** — new apps in Development
Mode no longer get them by default. This export intentionally sticks to
endpoints that work for any app (playlists, tracks, albums, artists), so
it won't hit that wall. If Spotify grants your app extended access later
and you want audio-feature data (tempo, key, energy, etc.) added to the
export, that's a small addition to `internal/spotifyapi` and
`internal/export`.

Also worth knowing: Spotify renamed the playlist-tracks endpoint from
`/playlists/{id}/tracks` to `/playlists/{id}/items` (the old path now
returns 403), and the field holding the track count on a playlist object
from `tracks` to `items`. Spotuify uses the current names; if you see 403s
or zeroed-out track counts again in the future, that endpoint is the first
place to check against Spotify's current docs.

## Project layout

```
cmd/spotuify/         entry point
internal/config/      .env loading + persistence (Settings screen writes here)
internal/auth/        Spotify OAuth (Authorization Code flow, token cache)
internal/spotifyapi/  Spotify Web API client + models, rate-limit handling
internal/export/      JSON/CSV/cover-art writers
internal/tui/         Bubble Tea UI — one screen per file (mainmenu.go,
                       settings.go, export_screen.go), sharing chrome.go
                       (gradient logo, panel/help-bar layout), styles.go
                       (adaptive color palette), and keys.go (all
                       keybindings, as bubbles/key.Binding sets — add a
                       keymap here for any new screen and the help bar
                       picks it up automatically)
```
