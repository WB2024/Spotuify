package library

import "context"

// MusicBrainzResolver bridges a Spotify track's ISRC to any MusicBrainz
// Recording IDs associated with it, caching results to disk (see
// mbcache.go) so repeat lookups for the same ISRC — across playlists, or
// across runs — never touch the network again.
//
// Deliberately scoped this way (per-ISRC, called only for tracks a match
// pass couldn't already resolve by tag) rather than eagerly resolving
// every MusicBrainz-tagged file in the whole library up front: MusicBrainz's
// rate limit is a hard 1 request/second, so eagerly resolving a library of
// tens of thousands of tracks can take hours before any matching even
// starts. Scoped to however many tracks in the playlist(s) actually being
// matched didn't already match by tag, the cost is proportional to what
// the user is doing right now, not the size of their whole collection.
type MusicBrainzResolver struct {
	client     *musicbrainzClient
	cachePath  string
	cache      mbCache
	sinceFlush int
}

// NewMusicBrainzResolver loads (or creates) the on-disk cache at cachePath.
func NewMusicBrainzResolver(cachePath string) *MusicBrainzResolver {
	return &MusicBrainzResolver{
		client:    newMusicBrainzClient(),
		cachePath: cachePath,
		cache:     loadMBCache(cachePath),
	}
}

// RecordingsForISRC returns the MusicBrainz Recording IDs associated with
// isrc, from cache if known, otherwise via the (rate-limited) API — which
// is then cached, flushing to disk every 20 new lookups so a cancelled run
// doesn't lose progress.
func (r *MusicBrainzResolver) RecordingsForISRC(ctx context.Context, isrc string) ([]string, error) {
	if e, ok := r.cache.Entries[isrc]; ok && e.Resolved {
		return e.RecordingIDs, nil
	}

	ids, err := r.client.RecordingsForISRC(ctx, isrc)
	if err != nil {
		return nil, err
	}

	r.cache.Entries[isrc] = mbCacheEntry{RecordingIDs: ids, Resolved: true}
	r.sinceFlush++
	if r.sinceFlush >= 20 {
		_ = saveMBCache(r.cachePath, r.cache)
		r.sinceFlush = 0
	}
	return ids, nil
}

// Close persists any not-yet-flushed cache entries. Call it when done with
// the resolver (e.g. after a batch of matching runs completes or the app
// is shutting the screen down).
func (r *MusicBrainzResolver) Close() error {
	if r.sinceFlush == 0 {
		return nil
	}
	return saveMBCache(r.cachePath, r.cache)
}
