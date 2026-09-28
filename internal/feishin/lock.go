package feishin

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// checkNotRunning reports an error if Feishin currently has leveldbPath
// open. It matters a lot how this is checked: an earlier version of this
// package assumed goleveldb.OpenFile would itself fail if Feishin (or
// anything else) already had the database open, since LevelDB is meant to
// allow only one writer. That's true within a single LevelDB
// implementation, but Chromium's own (C++) LevelDB and goleveldb enforce it
// with different, mutually invisible OS locking primitives - fcntl byte-range
// locks vs flock - so goleveldb's own open-time check saw no conflict at all
// and happily opened (and wrote to, and compacted) a database Feishin still
// had open. Confirmed live: it didn't visibly corrupt anything that time,
// but two independent LevelDB implementations writing to the same files
// with no shared lock is exactly the kind of thing that eventually does.
//
// So this checks Chromium's actual mechanism directly, with F_GETLK - a
// pure probe of the OS lock table (asks the kernel who holds a lock, never
// acquires one itself) - against the same LOCK file Chromium's LevelDB
// locks. This never opens the database at all, so there's no window for
// the race that caused the problem above: either this sees the lock and
// SyncPlaylistOrder never proceeds, or it doesn't and no real LevelDB
// implementation currently disagrees.
func checkNotRunning(leveldbPath string) error {
	lockPath := filepath.Join(leveldbPath, "LOCK")
	f, err := os.OpenFile(lockPath, os.O_RDWR, 0)
	if os.IsNotExist(err) {
		return fmt.Errorf("no Feishin local storage found at %s - check the path in Settings", leveldbPath)
	}
	if err != nil {
		return fmt.Errorf("checking whether Feishin is running: %w", err)
	}
	defer f.Close()

	lock := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: 0} // 0 = SEEK_SET: check the whole file, from the start
	if err := syscall.FcntlFlock(f.Fd(), syscall.F_GETLK, &lock); err != nil {
		return fmt.Errorf("checking whether Feishin is running: %w", err)
	}
	if lock.Type != syscall.F_UNLCK {
		return fmt.Errorf("Feishin is currently running (pid %d has its local storage open) - close it and try again", lock.Pid)
	}
	return nil
}
