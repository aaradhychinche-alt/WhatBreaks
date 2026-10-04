package config

import (
	"os"
	"strings"
)

// EnvLookup represents a function that retrieves an environment variable by name.
// It returns the value and whether the variable was set.
type EnvLookup func(key string) (string, bool)

// OsEnv returns an EnvLookup backed by the real operating system environment.
func OsEnv() EnvLookup {
	return os.LookupEnv
}

// MapEnv returns an EnvLookup backed by a static map, ideal for unit testing.
func MapEnv(m map[string]string) EnvLookup {
	return func(key string) (string, bool) {
		if m == nil {
			return "", false
		}
		val, ok := m[key]
		return val, ok
	}
}

// MultiEnv returns an EnvLookup that queries multiple lookups in order, returning the first match.
func MultiEnv(lookups ...EnvLookup) EnvLookup {
	return func(key string) (string, bool) {
		for _, lookup := range lookups {
			if lookup == nil {
				continue
			}
			if val, ok := lookup(key); ok {
				return val, true
			}
		}
		return "", false
	}
}

// LookupWithFallback queries primary key first, then fallback keys in order.
func LookupWithFallback(env EnvLookup, primary string, fallbacks ...string) (string, bool) {
	if env == nil {
		return "", false
	}
	if val, ok := env(primary); ok && strings.TrimSpace(val) != "" {
		return val, true
	}
	for _, fb := range fallbacks {
		if val, ok := env(fb); ok && strings.TrimSpace(val) != "" {
			return val, true
		}
	}
	// If set but empty, check if primary was present
	if val, ok := env(primary); ok {
		return val, true
	}
	for _, fb := range fallbacks {
		if val, ok := env(fb); ok {
			return val, true
		}
	}
	return "", false
}

// HasKey checks if the environment contains any of the specified keys.
func HasKey(env EnvLookup, keys ...string) (string, bool) {
	if env == nil {
		return "", false
	}
	for _, key := range keys {
		if _, ok := env(key); ok {
			return key, true
		}
	}
	return "", false
}
