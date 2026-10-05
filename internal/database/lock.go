package database

import (
	"context"
	"unicode/utf16"
)

// Hash32 computes the 32-bit signed integer hash matching the legacy JavaScript hash32 algorithm:
//
//	function hash32(s) {
//	  let h = 0;
//	  for (let i = 0; i < s.length; i++) {
//	    h = (h << 5) - h + s.charCodeAt(i);
//	    h |= 0;
//	  }
//	  return h;
//	}
//
// In JavaScript, string indexing and charCodeAt operate on UTF-16 code units.
// We encode Go's UTF-8 runes to UTF-16 code units to ensure exact byte-for-byte
// and character-for-character parity across ASCII, BMP Unicode, and Astral emojis.
func Hash32(s string) int32 {
	var h int32
	for _, codeUnit := range utf16.Encode([]rune(s)) {
		h = (h << 5) - h + int32(codeUnit)
	}
	return h
}

// AdvisoryLockKey computes Math.abs(hash32(key)) as a 64-bit integer,
// preserving the exact parameter value sent by Node.js workers/runtime/db.js to PostgreSQL pg_try_advisory_lock.
func AdvisoryLockKey(key string) int64 {
	h := int64(Hash32(key))
	if h < 0 {
		return -h
	}
	return h
}

// TryAdvisoryLock executes pg_try_advisory_lock on the provided connection session,
// guaranteeing connection affinity and non-blocking lock acquisition matching workers/runtime/db.js.
func TryAdvisoryLock(ctx context.Context, conn Connection, key string) (bool, error) {
	if conn == nil {
		return false, ErrDatabaseNotReady
	}
	return conn.TryAdvisoryLock(ctx, key)
}

// AdvisoryUnlock executes pg_advisory_unlock on the provided connection session,
// releasing the lock previously acquired on the same connection.
func AdvisoryUnlock(ctx context.Context, conn Connection, key string) (bool, error) {
	if conn == nil {
		return false, ErrDatabaseNotReady
	}
	return conn.AdvisoryUnlock(ctx, key)
}
