package auth

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/config"
)

// LocalDevCORSOrigins matches LOCAL_DEV_CORS_ORIGINS from infrastructure/auth/session-cookie-options.js.
var LocalDevCORSOrigins = []string{
	"http://localhost:3000",
	"http://localhost:5173",
	"http://localhost:4000",
	"http://localhost:8080",
	"http://127.0.0.1:3000",
	"http://127.0.0.1:5173",
	"http://127.0.0.1:4000",
	"http://127.0.0.1:8080",
}

// CookieConfig encapsulates session and CSRF cookie security attributes.
type CookieConfig struct {
	HTTPOnly bool
	SameSite http.SameSite
	Secure   bool
	MaxAge   time.Duration
	Domain   string
	Path     string
}

// SameSiteString returns the lowercase cookie attribute string.
func (c CookieConfig) SameSiteString() string {
	switch c.SameSite {
	case http.SameSiteNoneMode:
		return "none"
	case http.SameSiteStrictMode:
		return "strict"
	case http.SameSiteLaxMode:
		fallthrough
	default:
		return "lax"
	}
}

func parseBooleanEnv(value string) (bool, bool) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "true" {
		return true, true
	}
	if normalized == "false" {
		return false, true
	}
	return false, false
}

// NormalizeOrigin parses a URL and returns its scheme://host[:port] origin.
func NormalizeOrigin(value string) string {
	val := strings.TrimSpace(value)
	if val == "" {
		return ""
	}
	u, err := url.Parse(val)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return fmt.Sprintf("%s://%s", u.Scheme, u.Host)
}

// IsLocalHTTPOrigin reports whether an origin is an unencrypted HTTP localhost/loopback address.
func IsLocalHTTPOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]"
}

// IsHTTPSOrigin reports whether an origin uses secure HTTPS.
func IsHTTPSOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Scheme == "https"
}

// ShouldUseCrossOriginCookies determines if cross-origin cookies (SameSite=None, Secure=true)
// are required for split-host HTTPS deployments.
func ShouldUseCrossOriginCookies(apiOrigin, appOrigin string) bool {
	if apiOrigin == "" || appOrigin == "" || apiOrigin == appOrigin {
		return false
	}
	if IsLocalHTTPOrigin(apiOrigin) && IsLocalHTTPOrigin(appOrigin) {
		return false
	}
	return IsHTTPSOrigin(apiOrigin) && IsHTTPSOrigin(appOrigin)
}

// ResolveEffectiveOrigins extracts and normalizes the effective APP_URL and API_URL origins.
func ResolveEffectiveOrigins(env config.EnvLookup) (apiOrigin, appOrigin string) {
	if env == nil {
		env = config.OsEnv()
	}
	appVal, _ := env("APP_URL")
	apiVal, _ := env("API_URL")

	appOrigin = NormalizeOrigin(appVal)
	apiOrigin = NormalizeOrigin(apiVal)
	if apiOrigin == "" {
		apiOrigin = appOrigin
	}
	return apiOrigin, appOrigin
}

// ResolveProductionSecure determines whether cookies must require the Secure attribute.
func ResolveProductionSecure(env config.EnvLookup) bool {
	if env == nil {
		env = config.OsEnv()
	}
	nodeEnv, _ := env("NODE_ENV")
	isProduction := strings.ToLower(strings.TrimSpace(nodeEnv)) == "production"
	if !isProduction {
		return false
	}

	overrideVal, _ := env("SESSION_COOKIE_SECURE_LOCALHOST_OVERRIDE")
	override, ok := parseBooleanEnv(overrideVal)
	if !ok || !override {
		return true
	}

	apiOrigin, appOrigin := ResolveEffectiveOrigins(env)
	if IsLocalHTTPOrigin(apiOrigin) && IsLocalHTTPOrigin(appOrigin) {
		return false
	}

	return true
}

// ResolveSessionCookieOptions builds the session cookie configuration matching
// infrastructure/auth/session-cookie-options.js resolveSessionCookieOptions.
func ResolveSessionCookieOptions(env config.EnvLookup) CookieConfig {
	if env == nil {
		env = config.OsEnv()
	}

	productionSecure := ResolveProductionSecure(env)
	apiOrigin, appOrigin := ResolveEffectiveOrigins(env)
	useCrossOrigin := ShouldUseCrossOriginCookies(apiOrigin, appOrigin)

	cookie := CookieConfig{
		HTTPOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   productionSecure,
		MaxAge:   2 * time.Hour,
		Path:     "/",
	}

	domainVal, _ := env("SESSION_COOKIE_DOMAIN")
	explicitDomain := strings.TrimSpace(domainVal)
	if explicitDomain != "" {
		if strings.HasPrefix(explicitDomain, ".") {
			cookie.Domain = explicitDomain
		} else {
			cookie.Domain = "." + explicitDomain
		}
	}

	if useCrossOrigin {
		cookie.SameSite = http.SameSiteNoneMode
		cookie.Secure = true
	}

	return cookie
}

// ResolveClearSessionCookieOptions produces options required to expire the session cookie.
func ResolveClearSessionCookieOptions(env config.EnvLookup) CookieConfig {
	cfg := ResolveSessionCookieOptions(env)
	cfg.MaxAge = -1 * time.Second
	return cfg
}

// ResolveCsrfCookieName returns __Host-psifi.x-csrf-token in production with secure cookies
// and no custom domain; otherwise x-csrf-token.
func ResolveCsrfCookieName(env config.EnvLookup, sessionCookie CookieConfig) string {
	if env == nil {
		env = config.OsEnv()
	}
	nodeEnv, _ := env("NODE_ENV")
	isProduction := strings.ToLower(strings.TrimSpace(nodeEnv)) == "production"
	productionSecure := ResolveProductionSecure(env)

	if isProduction && productionSecure && sessionCookie.Domain == "" {
		return "__Host-psifi.x-csrf-token"
	}
	return "x-csrf-token"
}

// BuildCorsOrigins aggregates allowed CORS origins matching infrastructure/auth/session-cookie-options.js.
func BuildCorsOrigins(env config.EnvLookup) []string {
	if env == nil {
		env = config.OsEnv()
	}

	apiOrigin, appOrigin := ResolveEffectiveOrigins(env)
	seen := make(map[string]bool)
	var origins []string

	add := func(origin string) {
		if origin != "" && !seen[origin] {
			seen[origin] = true
			origins = append(origins, origin)
		}
	}

	if appOrigin != "" {
		add(appOrigin)
	} else {
		add("http://localhost:5173")
	}

	if apiOrigin != "" {
		add(apiOrigin)
	}

	nodeEnv, _ := env("NODE_ENV")
	isProd := strings.ToLower(strings.TrimSpace(nodeEnv)) == "production"
	allowLocalVal, _ := env("ALLOW_LOCAL_DEV_CORS")
	allowLocal, _ := parseBooleanEnv(allowLocalVal)

	if !isProd || allowLocal {
		for _, devOrigin := range LocalDevCORSOrigins {
			add(devOrigin)
		}
	}

	return origins
}

// ToHTTPCookie creates a standard Go http.Cookie from CookieConfig.
func ToHTTPCookie(name, value string, cfg CookieConfig) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     cfg.Path,
		Domain:   cfg.Domain,
		Expires:  time.Now().Add(cfg.MaxAge),
		MaxAge:   int(cfg.MaxAge.Seconds()),
		Secure:   cfg.Secure,
		HttpOnly: cfg.HTTPOnly,
		SameSite: cfg.SameSite,
	}
}
