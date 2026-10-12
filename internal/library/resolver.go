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

// MBISRCBatchSize is the most ISRCs RecordingsForISRCs will ever put in one
// MusicBrainz search request - see musicbrainzClient.RecordingsForISRCs for
// why that request shape is safe up to (and a bit past) this size. Callers
// batching a larger pending list (see match.All) should chunk into groups
// of at most this size themselves, both to get a progress update after each
// chunk and because RecordingsForISRCs only sub-chunks defensively, not for
// efficiency - one call per chunk is the point.
const MBISRCBatchSize = 40

// RecordingsForISRCs resolves many ISRCs at once: whatever's already cached
// is returned immediately with no network call at all, and the rest is
// resolved via as few MusicBrainz search requests as possible (chunks of at
// most MBISRCBatchSize - see musicbrainzClient.RecordingsForISRCs) instead
// of one request per ISRC. This is what makes bridging a few thousand
// unmatched tracks in one big batch take well under a minute of MusicBrainz
// calls instead of the better part of an hour at 1 request/second serially.
//
// A chunk that fails outright (network error, etc.) is left unresolved in
// the cache rather than aborting the whole call - those ISRCs just won't
// have a tier-2 match this run, same leniency RecordingsForISRC already has
// per-lookup, and they'll be retried on the next run since nothing gets
// marked Resolved for them.
func (r *MusicBrainzResolver) RecordingsForISRCs(ctx context.Context, isrcs []string) map[string][]string {
	out := make(map[string][]string, len(isrcs))

	var pending []string
	seen := make(map[string]bool, len(isrcs))
	for _, isrc := range isrcs {
		if isrc == "" || seen[isrc] {
			continue
		}
		seen[isrc] = true
		if e, ok := r.cache.Entries[isrc]; ok && e.Resolved {
			if len(e.RecordingIDs) > 0 {
				out[isrc] = e.RecordingIDs
			}
			continue
		}
		pending = append(pending, isrc)
	}

	for start := 0; start < len(pending); start += MBISRCBatchSize {
		if ctx.Err() != nil {
			return out
		}
		end := start + MBISRCBatchSize
		if end > len(pending) {
			end = len(pending)
		}
		batch := pending[start:end]

		resolved, err := r.client.RecordingsForISRCs(ctx, batch)
		if err != nil {
			continue
		}
		for _, isrc := range batch {
			ids := resolved[isrc]
			r.cache.Entries[isrc] = mbCacheEntry{RecordingIDs: ids, Resolved: true}
			r.noteChange()
			if len(ids) > 0 {
				out[isrc] = ids
			}
		}
	}

	return out
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
	r.noteChange()
	return ids, nil
}

// ReleaseGroupsForRecording returns the release groups (albums, singles,
// compilations...) a MusicBrainz recording appears on, from cache if
// known, otherwise via the rate-limited API — the bridge from a Spotify
// track's ISRC to the exact album Lidarr should be asked for.
func (r *MusicBrainzResolver) ReleaseGroupsForRecording(ctx context.Context, recordingID string) ([]ReleaseGroup, error) {
	if e, ok := r.cache.ReleaseGroups[recordingID]; ok && e.Resolved {
		return e.Groups, nil
	}

	groups, err := r.client.ReleaseGroupsForRecording(ctx, recordingID)
	if err != nil {
		return nil, err
	}

	r.cache.ReleaseGroups[recordingID] = mbRGEntry{Groups: groups, Resolved: true}
	r.noteChange()
	return groups, nil
}

func (r *MusicBrainzResolver) noteChange() {
	r.sinceFlush++
	if r.sinceFlush >= 20 {
		_ = saveMBCache(r.cachePath, r.cache)
		r.sinceFlush = 0
	}
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
