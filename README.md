# Spotuify

A terminal UI for Spotify playlists. Two things so far:

- **Export** — full playlist metadata, complete tracklists (with
  added-by/added-at), track & album metadata, and the highest-resolution
  cover art available, to JSON and CSV.
- **Match to Local Library** — map each track in a Spotify playlist to a
  file in your own local music library (via a Navidrome server's already-
  scanned metadata) and write a portable `.m3u8` playlist, so you can play
  your Spotify playlists from files you actually own.

Built with [Bubble Tea](https://github.com/charmbracelet/bubbletea).

## Status

Early. More features can be layered onto this foundation over time.

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

4. **Navidrome (optional — only needed for "Match to Local Library").** If
   you run a [Navidrome](https://www.navidrome.org/) server over your music
   collection, set `SPOTUIFY_NAVIDROME_DB` (its `navidrome.db` path) and
   `SPOTUIFY_NAVIDROME_MUSIC_PATH` (where that server's music folder is
   reachable from this machine) in `.env`, or fill them in from Settings.
   Not needed at all if you only want the JSON/CSV export.

5. **Fetch dependencies and build:**

   ```bash
   go mod tidy
   go build ./cmd/spotuify
   ```

6. **Run it:**

   ```bash
   ./spotuify
   ```

   You land on a main menu: **Export Playlists**, **Match to Local
   Library**, and **Settings**. Picking either of the first two is what
   triggers Spotify login the first time (opens your browser, read-only
   access to your playlists — see Scopes below). After that, a refresh
   token is cached in your OS user-cache directory (e.g.
   `~/.cache/spotuify/token.json` on Linux) so you won't be asked
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

**Match to Local Library:**
- Same navigation as Export Playlists (arrow keys, `space`, `a`, `/`, `esc`)
- `enter` — match the checked (or highlighted) playlists against your
  local library and write `.m3u8` files. The first time this runs, it
  loads and indexes your Navidrome library (see below) — after that it's
  cached and reused for the rest of the session.
- Shows a live table, one row per track, as each playlist is matched:
  method (`isrc`/`fuzzy`/`missing`) and which local file it matched to.

**Settings:**
- `↑`/`↓` — move between fields
- `enter` — edit the highlighted field (text fields), or activate an action
  row (Log out / Save / Back)
- while editing a field: type normally, `enter`/`esc` to confirm and stop
  editing
- `space` — toggle a checkbox field (e.g. Download cover art)
- `esc` (when not editing) — back to the main menu

Settings lets you set/change your Spotify Client ID and Secret, the export
directory, the OAuth redirect port, whether cover art is downloaded, your
Navidrome database path and music folder, the playlist output directory,
and the two matching toggles (MusicBrainz resolution, fuzzy matching) — all
written back to `.env` on Save. Changing credentials or hitting "Log out"
clears the cached session, so the next export/match re-triggers browser
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

## How matching to a local library works

The goal: for every track in a Spotify playlist, find the corresponding
file in your own collection, as reliably as possible — not a best-effort
guess based on song titles.

### Why Navidrome, not scanning your files directly

[Navidrome](https://www.navidrome.org/) has almost certainly already
scanned and tagged your whole library (it needs to, to serve it). Spotuify
reads its SQLite database directly — `media_file` — rather than re-opening
every audio file and re-parsing tags itself. That's both simpler and
faster than a from-scratch filesystem scan, and it means Spotuify is
reading exactly the metadata Navidrome itself uses, including any
MusicBrainz IDs Navidrome's own scanner already extracted.

**Path mapping.** Navidrome stores each track's path relative to its music
root (`ND_MUSICFOLDER` in its own environment — typically something like
`/music` inside its container). Settings' **Navidrome music path** is
where that same folder is reachable from wherever Spotuify runs — the same
idea as a Radarr/Sonarr/Lidarr remote path mapping. A local file's real
path is just `filepath.Join(<Navidrome music path>, <path from the
database>)`. The database is opened strictly read-only
(`_query_only=1`) — Spotuify never writes to it.

**If the database won't open** (a known issue on Docker/NAS setups — the
live file can be owned by a different container UID, or SQLite's file
locking can be unreliable over a network filesystem like NFS/CIFS): Spotuify
automatically falls back to copying the database to a local temp directory
and opening that instead, cleaned up afterward. The copy includes the
`-wal`/`-shm` sidecar files alongside the main one, not just the `.db` file
— Navidrome runs SQLite in WAL mode, where a large amount of recently
written data can sit in the `-wal` file for a long time before being
checkpointed back into the main file, so copying only the `.db` file risks
silently working from a stale snapshot. This only helps if the file is
genuinely readable but something about opening it live is unreliable — a
real permission problem (wrong owner/group) still needs a manual `chown`/
`chmod` fix, which is what the resulting error message will tell you.

### The matching pipeline

Matching is tiered, from most to least certain:

1. **ISRC.** Spotify reports an [ISRC](https://en.wikipedia.org/wiki/International_Standard_Recording_Code)
   (`external_ids.isrc`) on essentially every track — it's the industry's
   own cross-service recording identifier, not something Spotuify derives.
   On the local side, Navidrome extracts ISRC directly from a file's own
   tags when present (stored in its `media_file.tags` JSON as
   `tags.isrc[]`). If both sides have the same ISRC, that's as close to a
   certain match as this gets.

2. **MusicBrainz bridge.** Many files carry a MusicBrainz Recording ID
   (`mbz_recording_id`, written by taggers like Picard) but no ISRC tag of
   their own. For those, Spotuify looks the recording up via the
   MusicBrainz API (`GET /ws/2/recording/{mbid}?inc=isrcs`), which returns
   every ISRC MusicBrainz has on file for that recording — and checks
   those against the Spotify track's ISRC too. This is still tier 1 in
   effect (ISRC-certain), it's just that the local side's ISRC had to be
   fetched rather than read straight off the file. Looked into this
   before building it: neither the MusicBrainz nor the ListenBrainz API
   has a *direct* "give me a Spotify ID for this MusicBrainz ID" endpoint
   (ListenBrainz's `metadata/lookup` goes the other way, text → MBID) — so
   ISRC is the real bridge between the two services, not a shortcut around
   one.

   This step is rate-limited to MusicBrainz's documented 1 request/second
   and **cached indefinitely** (an MBID's ISRCs don't change), so it only
   costs time the first time a given MusicBrainz ID is seen — cached in
   `~/.cache/spotuify/mb_isrc_cache.json`, incrementally flushed every 20
   lookups so a cancelled run doesn't lose progress. On a large library
   with many MusicBrainz-tagged-but-not-ISRC-tagged files, this first pass
   can take a while (thousands of files ÷ 1/second adds up) — `esc` cancels
   cleanly at any point and resumes from where it left off next time. Turn
   off "Resolve MusicBrainz IDs via API" in Settings to skip this entirely
   and rely on direct ISRC tags + fuzzy matching only.

3. **Fuzzy fallback.** For anything left unmatched — no ISRC either
   side — a normalized artist/title similarity score (Levenshtein-based,
   after stripping punctuation, case, and common noise like "remastered"
   or "radio edit") against the whole library, requiring a fairly high
   score (≥0.82) to count as a match. Each local file is only ever
   assigned to one Spotify track per run, so near-duplicate local files
   can't all silently claim the same track. Turn off "Fuzzy-match tracks
   with no ISRC/MusicBrainz data" in Settings to require ISRC certainty
   only.

Anything that clears neither tier is reported as missing, not guessed at.

### Output

For each playlist, under `<M3U8 output directory>/<playlist name>/`:

- **`<playlist name>.m3u8`** — every matched track, in playlist order,
  with `#EXTINF` duration/artist/title and a path *relative to the `.m3u8`
  file itself* — the convention VLC, foobar2000, Kodi, and Plex all expect
  for a playlist that stays valid if the whole folder is moved. Also
  includes a `#PLAYLIST:<name>` directive — that's what Navidrome uses as
  the playlist's display name when it scans the file in (see below), not
  the filename.
- **`missing.txt`** — a human-readable list of anything that couldn't be
  matched (artist, title, album, Spotify link), only written if there's
  anything to report.
- **`cover.jpg`** — the playlist's cover art, same as the JSON/CSV export.

### Navidrome picks these up automatically

If the M3U8 output directory is inside Navidrome's own music folder (as it
is by default here — the Playlists folder sits under the same root
Navidrome watches), **you don't need to do anything else**: Navidrome
scans for `.m3u8` files as part of its normal library scan
(`ND_SCANNER_SCHEDULE` in its own config) and creates/updates a matching
entry in its own `playlist` table itself, resolving every relative path
back to the actual file it already indexed. Spotuify never writes to
Navidrome's database directly — verified this by checking `playlist` and
`playlist_tracks` after a real run: Navidrome had already picked up the
file, named the playlist from the `#PLAYLIST:` line, and joined every
track to the right `media_file` row on its own. If you rename or delete a
playlist's folder, Navidrome's own scanner reconciles that too (it purges
playlists whose backing file has disappeared, if `ND_SCANNER_PURGEMISSING`
is enabled) — on its own schedule, or immediately if you trigger a scan
manually from Navidrome's UI.

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
internal/export/      JSON/CSV writers for the Export screen
internal/library/     loads a matchable index from Navidrome's SQLite database
                       (navidrome.go) + the MusicBrainz ISRC-resolution client
                       and its cache (musicbrainz.go, mbcache.go)
internal/match/       the ISRC/fuzzy matching engine (match.go, normalize.go)
internal/m3u8/        writes matched playlists as .m3u8 + missing-track
                       reports + cover art, one folder per playlist
internal/coverart/     shared cover-art downloader (used by export and m3u8)
internal/tui/          Bubble Tea UI — one screen per file (mainmenu.go,
                        settings.go, export_screen.go, match_screen.go),
                        sharing chrome.go (gradient logo, panel/help-bar
                        layout), styles.go (adaptive color palette), and
                        keys.go (all keybindings, as bubbles/key.Binding
                        sets — add a keymap here for any new screen and the
                        help bar picks it up automatically)
```
