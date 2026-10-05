package auth

import (
	"net/http"
	"testing"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/config"
)

func TestResolveSessionCookieOptions_ProductionDefault(t *testing.T) {
	env := config.MapEnv(map[string]string{
		"NODE_ENV": "production",
	})

	cfg := ResolveSessionCookieOptions(env)
	if !cfg.Secure {
		t.Errorf("expected Secure=true in production")
	}
	if !cfg.HTTPOnly {
		t.Errorf("expected HTTPOnly=true")
	}
	if cfg.SameSite != http.SameSiteLaxMode {
		t.Errorf("expected SameSite=Lax by default, got %v", cfg.SameSite)
	}
	if cfg.MaxAge != 2*time.Hour {
		t.Errorf("expected MaxAge=2h, got %v", cfg.MaxAge)
	}
	if cfg.Path != "/" {
		t.Errorf("expected Path=/, got %q", cfg.Path)
	}
	if cfg.Domain != "" {
		t.Errorf("expected empty domain when not configured, got %q", cfg.Domain)
	}
}

func TestResolveSessionCookieOptions_DevelopmentDefault(t *testing.T) {
	env := config.MapEnv(map[string]string{
		"NODE_ENV": "development",
	})

	cfg := ResolveSessionCookieOptions(env)
	if cfg.Secure {
		t.Errorf("expected Secure=false in development")
	}
	if !cfg.HTTPOnly {
		t.Errorf("expected HTTPOnly=true in development")
	}
	if cfg.SameSite != http.SameSiteLaxMode {
		t.Errorf("expected SameSite=Lax")
	}
}

func TestResolveSessionCookieOptions_DomainHandling(t *testing.T) {
	// Without leading dot
	env1 := config.MapEnv(map[string]string{
		"SESSION_COOKIE_DOMAIN": "whatbreaks.com",
	})
	cfg1 := ResolveSessionCookieOptions(env1)
	if cfg1.Domain != ".whatbreaks.com" {
		t.Errorf("expected .whatbreaks.com, got %q", cfg1.Domain)
	}

	// With leading dot already
	env2 := config.MapEnv(map[string]string{
		"SESSION_COOKIE_DOMAIN": ".whatbreaks.com",
	})
	cfg2 := ResolveSessionCookieOptions(env2)
	if cfg2.Domain != ".whatbreaks.com" {
		t.Errorf("expected .whatbreaks.com, got %q", cfg2.Domain)
	}
}

func TestResolveSessionCookieOptions_LocalhostOverride(t *testing.T) {
	// Override enabled with local origins
	envOverride := config.MapEnv(map[string]string{
		"NODE_ENV": "production",
		"SESSION_COOKIE_SECURE_LOCALHOST_OVERRIDE": "true",
		"APP_URL": "http://localhost:3000",
		"API_URL": "http://localhost:4000",
	})
	cfg := ResolveSessionCookieOptions(envOverride)
	if cfg.Secure {
		t.Errorf("expected Secure=false when override is enabled for localhost origins")
	}

	// Override enabled with real https origins should still be Secure
	envHttps := config.MapEnv(map[string]string{
		"NODE_ENV": "production",
		"SESSION_COOKIE_SECURE_LOCALHOST_OVERRIDE": "true",
		"APP_URL": "https://app.whatbreaks.com",
		"API_URL": "https://api.whatbreaks.com",
	})
	cfgHttps := ResolveSessionCookieOptions(envHttps)
	if !cfgHttps.Secure {
		t.Errorf("expected Secure=true for https origins despite localhost override")
	}
}

func TestResolveSessionCookieOptions_CrossOriginCookies(t *testing.T) {
	// Split HTTPS deployment requires SameSite=None, Secure=true
	envSplit := config.MapEnv(map[string]string{
		"NODE_ENV": "production",
		"APP_URL":  "https://app.whatbreaks.com",
		"API_URL":  "https://api.whatbreaks.com",
	})
	cfg := ResolveSessionCookieOptions(envSplit)
	if cfg.SameSite != http.SameSiteNoneMode {
		t.Errorf("expected SameSite=None for split HTTPS deployment, got %v", cfg.SameSite)
	}
	if !cfg.Secure {
		t.Errorf("expected Secure=true for SameSite=None")
	}

	// Split local HTTP ports stay Lax
	envLocalSplit := config.MapEnv(map[string]string{
		"APP_URL": "http://localhost:3000",
		"API_URL": "http://localhost:4000",
	})
	cfgLocal := ResolveSessionCookieOptions(envLocalSplit)
	if cfgLocal.SameSite != http.SameSiteLaxMode {
		t.Errorf("expected SameSite=Lax for local HTTP split ports, got %v", cfgLocal.SameSite)
	}
}

func TestResolveClearSessionCookieOptions(t *testing.T) {
	env := config.MapEnv(map[string]string{
		"NODE_ENV": "production",
	})
	clearCfg := ResolveClearSessionCookieOptions(env)
	if clearCfg.MaxAge >= 0 {
		t.Errorf("expected negative MaxAge for clearing cookie, got %v", clearCfg.MaxAge)
	}
	if clearCfg.Path != "/" {
		t.Errorf("expected Path=/, got %q", clearCfg.Path)
	}
}

func TestResolveCsrfCookieName(t *testing.T) {
	// Production, Secure, No custom domain -> __Host-psifi.x-csrf-token
	envProd := config.MapEnv(map[string]string{
		"NODE_ENV": "production",
	})
	cookieProd := ResolveSessionCookieOptions(envProd)
	csrfProd := ResolveCsrfCookieName(envProd, cookieProd)
	if csrfProd != "__Host-psifi.x-csrf-token" {
		t.Errorf("expected __Host-psifi.x-csrf-token, got %q", csrfProd)
	}

	// Production with custom domain -> x-csrf-token (__Host- prefix cannot use Domain)
	envProdDomain := config.MapEnv(map[string]string{
		"NODE_ENV":              "production",
		"SESSION_COOKIE_DOMAIN": "whatbreaks.com",
	})
	cookieProdDomain := ResolveSessionCookieOptions(envProdDomain)
	csrfProdDomain := ResolveCsrfCookieName(envProdDomain, cookieProdDomain)
	if csrfProdDomain != "x-csrf-token" {
		t.Errorf("expected x-csrf-token when domain is set, got %q", csrfProdDomain)
	}

	// Development -> x-csrf-token
	envDev := config.MapEnv(map[string]string{
		"NODE_ENV": "development",
	})
	cookieDev := ResolveSessionCookieOptions(envDev)
	csrfDev := ResolveCsrfCookieName(envDev, cookieDev)
	if csrfDev != "x-csrf-token" {
		t.Errorf("expected x-csrf-token in dev, got %q", csrfDev)
	}
}

func TestBuildCorsOrigins(t *testing.T) {
	// 1. Development includes local dev origins
	envDev := config.MapEnv(map[string]string{
		"NODE_ENV": "development",
		"APP_URL":  "http://localhost:5173",
	})
	originsDev := BuildCorsOrigins(envDev)
	found3000 := false
	for _, o := range originsDev {
		if o == "http://localhost:3000" {
			found3000 = true
			break
		}
	}
	if !found3000 {
		t.Errorf("expected local dev port 3000 to be in dev CORS origins")
	}

	// 2. Production without override does not include arbitrary local dev ports
	envProd := config.MapEnv(map[string]string{
		"NODE_ENV": "production",
		"APP_URL":  "https://app.whatbreaks.com",
		"API_URL":  "https://api.whatbreaks.com",
	})
	originsProd := BuildCorsOrigins(envProd)
	if len(originsProd) != 2 {
		t.Errorf("expected exactly 2 origins in prod, got %d: %v", len(originsProd), originsProd)
	}

	// 3. Production with ALLOW_LOCAL_DEV_CORS=true includes local dev ports
	envProdOverride := config.MapEnv(map[string]string{
		"NODE_ENV":             "production",
		"APP_URL":              "https://app.whatbreaks.com",
		"ALLOW_LOCAL_DEV_CORS": "true",
	})
	originsProdOverride := BuildCorsOrigins(envProdOverride)
	if len(originsProdOverride) <= 2 {
		t.Errorf("expected local dev ports to be included when ALLOW_LOCAL_DEV_CORS=true")
	}
}

func TestToHTTPCookie(t *testing.T) {
	cfg := CookieConfig{
		HTTPOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   true,
		MaxAge:   3600 * time.Second,
		Path:     "/",
		Domain:   ".whatbreaks.com",
	}

	cookie := ToHTTPCookie("session_id", "session-val-123", cfg)
	if cookie.Name != "session_id" || cookie.Value != "session-val-123" {
		t.Errorf("cookie name or value mismatch: %+v", cookie)
	}
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie flags mismatch: %+v", cookie)
	}
	if cookie.Domain != ".whatbreaks.com" {
		t.Errorf("cookie domain mismatch: %q", cookie.Domain)
	}
}
