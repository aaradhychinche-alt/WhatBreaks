package config

import (
	"testing"
)

func TestNetwork_OfflineModeAndAllowlist(t *testing.T) {
	t.Run("online mode allows all hosts", func(t *testing.T) {
		cfg := &NetworkConfig{
			OfflineMode: false,
			Allowlist:   []string{},
		}
		if !IsAllowedHost(cfg, "api.github.com") {
			t.Errorf("expected online mode to allow external host")
		}
		if !IsAllowedHost(cfg, "10.0.0.1") {
			t.Errorf("expected online mode to allow internal IP")
		}
	})

	t.Run("offline mode with empty allowlist blocks all hosts", func(t *testing.T) {
		cfg := &NetworkConfig{
			OfflineMode: true,
			Allowlist:   []string{},
		}
		if IsAllowedHost(cfg, "api.github.com") {
			t.Errorf("expected empty allowlist in offline mode to block external host")
		}
		if IsAllowedHost(cfg, "localhost") {
			t.Errorf("expected empty allowlist in offline mode to block localhost")
		}
	})

	t.Run("offline mode matches exact, wildcard, and CIDR patterns", func(t *testing.T) {
		cfg := &NetworkConfig{
			OfflineMode: true,
			Allowlist: []string{
				"*.corp.local",
				"smtp.internal",
				"10.0.0.0/8",
				"192.168.1.0/24",
			},
		}

		// Exact match
		if !IsAllowedHost(cfg, "smtp.internal") {
			t.Errorf("expected exact match on smtp.internal")
		}
		if IsAllowedHost(cfg, "othersmtp.internal") {
			t.Errorf("expected othersmtp.internal to be blocked")
		}

		// Wildcard match
		if !IsAllowedHost(cfg, "auth.corp.local") {
			t.Errorf("expected auth.corp.local to match *.corp.local")
		}
		if !IsAllowedHost(cfg, "nested.sub.corp.local") {
			t.Errorf("expected nested.sub.corp.local to match *.corp.local")
		}
		if !IsAllowedHost(cfg, "corp.local") {
			t.Errorf("expected corp.local root to match *.corp.local")
		}
		if IsAllowedHost(cfg, "fakecorp.local") {
			t.Errorf("expected fakecorp.local not to match *.corp.local")
		}

		// CIDR match 10.0.0.0/8
		if !IsAllowedHost(cfg, "10.1.2.3") {
			t.Errorf("expected 10.1.2.3 to match 10.0.0.0/8")
		}
		if !IsAllowedHost(cfg, "10.254.254.254") {
			t.Errorf("expected 10.254.254.254 to match 10.0.0.0/8")
		}
		if IsAllowedHost(cfg, "11.0.0.1") {
			t.Errorf("expected 11.0.0.1 not to match 10.0.0.0/8")
		}

		// CIDR match 192.168.1.0/24
		if !IsAllowedHost(cfg, "192.168.1.55") {
			t.Errorf("expected 192.168.1.55 to match 192.168.1.0/24")
		}
		if IsAllowedHost(cfg, "192.168.2.1") {
			t.Errorf("expected 192.168.2.1 not to match 192.168.1.0/24")
		}
	})
}

func TestNetwork_IsInCIDR_EdgeCases(t *testing.T) {
	cases := []struct {
		name     string
		ip       string
		cidr     string
		expected bool
	}{
		{"valid match /32 exact IP", "192.168.1.1", "192.168.1.1/32", true},
		{"valid match /0 matches all", "1.2.3.4", "0.0.0.0/0", true},
		{"valid match /16", "172.16.50.1", "172.16.0.0/16", true},
		{"invalid match /16", "172.17.50.1", "172.16.0.0/16", false},
		{"malformed IP extra octet", "10.0.0.1.1", "10.0.0.0/8", false},
		{"malformed IP non-numeric", "10.0.0.abc", "10.0.0.0/8", false},
		{"malformed CIDR mask > 32", "10.0.0.1", "10.0.0.0/33", false},
		{"malformed CIDR mask negative", "10.0.0.1", "10.0.0.0/-1", false},
		{"malformed CIDR no mask", "10.0.0.1", "10.0.0.0", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsInCIDR(tc.ip, tc.cidr)
			if got != tc.expected {
				t.Fatalf("IsInCIDR(%q, %q) = %v, expected %v", tc.ip, tc.cidr, got, tc.expected)
			}
		})
	}
}

func TestNetwork_WebhookValidation(t *testing.T) {
	cfg := &NetworkConfig{
		WebhookAllowAllHosts: false,
		WebhookProviderHosts: []string{"custom.webhook.provider.com"},
		WebhookExtraHosts:    []string{"*.custom.internal"},
	}

	// Standard default provider hosts
	if !IsWebhookAllowed(cfg, "https://hooks.slack.com/services/T00/B00/X00") {
		t.Errorf("expected Slack webhook to be allowed")
	}
	if !IsWebhookAllowed(cfg, "https://discord.com/api/webhooks/123/abc") {
		t.Errorf("expected Discord webhook to be allowed")
	}
	if !IsWebhookAllowed(cfg, "https://events.pagerduty.com/v2/enqueue") {
		t.Errorf("expected PagerDuty webhook to be allowed")
	}

	// Custom provider hosts
	if !IsWebhookAllowed(cfg, "https://custom.webhook.provider.com/hook") {
		t.Errorf("expected custom webhook provider to be allowed")
	}
	if !IsWebhookAllowed(cfg, "https://alerts.custom.internal/notify") {
		t.Errorf("expected wildcard extra provider to be allowed")
	}

	// Disallowed hosts
	if IsWebhookAllowed(cfg, "https://unauthorized.attacker.com/leak") {
		t.Errorf("expected unauthorized host to be blocked")
	}
	if IsWebhookAllowed(cfg, "not-a-url") {
		t.Errorf("expected invalid URL to be rejected")
	}

	// WebhookAllowAllHosts=true
	cfgAll := &NetworkConfig{WebhookAllowAllHosts: true}
	if !IsWebhookAllowed(cfgAll, "https://anywhere.org/webhook") {
		t.Errorf("expected WebhookAllowAllHosts to permit any host")
	}
}

func TestNetwork_LoopbackAndPrivateIP(t *testing.T) {
	cases := []struct {
		ip       string
		expected bool
	}{
		{"127.0.0.1", true},
		{"::1", true},
		{"10.0.0.5", true},
		{"172.16.1.1", true},
		{"192.168.0.1", true},
		{"169.254.1.1", true}, // link-local
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"not-an-ip", false},
	}

	for _, tc := range cases {
		got := IsLoopbackOrPrivateIP(tc.ip)
		if got != tc.expected {
			t.Errorf("IsLoopbackOrPrivateIP(%q) = %v, expected %v", tc.ip, got, tc.expected)
		}
	}
}
