package config

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

// NetworkConfig captures outbound connectivity, allowlists, and webhook host policies.
type NetworkConfig struct {
	OfflineMode          bool
	Allowlist            []string
	UpdateCheckEnabled   bool
	WebhookAllowAllHosts bool
	WebhookProviderHosts []string
	WebhookExtraHosts    []string
}

// LoadNetworkConfig loads network policy configuration from the provided EnvLookup.
func LoadNetworkConfig(env EnvLookup) *NetworkConfig {
	if env == nil {
		env = OsEnv()
	}

	offlineVal, _ := LookupWithFallback(env, "OFFLINE_MODE")
	offlineMode := strings.TrimSpace(offlineVal) == "true"

	allowlistRaw, _ := LookupWithFallback(env, "OUTBOUND_ALLOWLIST")
	var allowlist []string
	if strings.TrimSpace(allowlistRaw) != "" {
		for _, part := range strings.Split(allowlistRaw, ",") {
			trimmed := strings.TrimSpace(part)
			if trimmed != "" {
				allowlist = append(allowlist, trimmed)
			}
		}
	}

	updateCheckVal, ok := LookupWithFallback(env, "UPDATE_CHECK_ENABLED")
	updateCheckEnabled := !offlineMode
	if ok && strings.TrimSpace(updateCheckVal) == "false" {
		updateCheckEnabled = false
	}

	webhookAllowAllVal, _ := LookupWithFallback(env, "WEBHOOK_ALLOW_ALL_HOSTS")
	webhookAllowAll := strings.TrimSpace(webhookAllowAllVal) == "true"

	providerHostsRaw, _ := LookupWithFallback(env, "WEBHOOK_PROVIDER_HOSTS")
	var providerHosts []string
	if strings.TrimSpace(providerHostsRaw) != "" {
		for _, part := range strings.Split(providerHostsRaw, ",") {
			trimmed := strings.TrimSpace(part)
			if trimmed != "" {
				providerHosts = append(providerHosts, trimmed)
			}
		}
	}

	extraHostsRaw, _ := LookupWithFallback(env, "WEBHOOK_EXTRA_PROVIDER_HOSTS")
	var extraHosts []string
	if strings.TrimSpace(extraHostsRaw) != "" {
		for _, part := range strings.Split(extraHostsRaw, ",") {
			trimmed := strings.TrimSpace(part)
			if trimmed != "" {
				extraHosts = append(extraHosts, trimmed)
			}
		}
	}

	return &NetworkConfig{
		OfflineMode:          offlineMode,
		Allowlist:            allowlist,
		UpdateCheckEnabled:   updateCheckEnabled,
		WebhookAllowAllHosts: webhookAllowAll,
		WebhookProviderHosts: providerHosts,
		WebhookExtraHosts:    extraHosts,
	}
}

// IsAllowedHost checks whether a hostname or IP is permitted for outbound connections.
// If offline mode is disabled, all outbound connections are permitted.
// If offline mode is enabled and allowlist is empty, all outbound connections are blocked.
func IsAllowedHost(cfg *NetworkConfig, hostname string) bool {
	if cfg == nil || !cfg.OfflineMode {
		return true
	}
	if len(cfg.Allowlist) == 0 {
		return false
	}

	for _, pattern := range cfg.Allowlist {
		if MatchPattern(hostname, pattern) {
			return true
		}
	}
	return false
}

// MatchPattern matches a hostname or IP against exact, wildcard (*.domain), or CIDR pattern.
func MatchPattern(hostname, pattern string) bool {
	trimmedHost := strings.ToLower(strings.TrimSpace(hostname))
	trimmedPattern := strings.ToLower(strings.TrimSpace(pattern))

	// Wildcard pattern: *.corp.local
	if strings.HasPrefix(trimmedPattern, "*.") {
		suffix := trimmedPattern[1:] // .corp.local
		root := trimmedPattern[2:]   // corp.local
		return strings.HasSuffix(trimmedHost, suffix) || trimmedHost == root
	}

	// CIDR pattern: 10.0.0.0/8
	if strings.Contains(trimmedPattern, "/") {
		return IsInCIDR(trimmedHost, trimmedPattern)
	}

	// Exact match
	return trimmedHost == trimmedPattern
}

// IsInCIDR evaluates whether an IPv4 address falls within a given CIDR notation.
// Matches the exact 32-bit bitmask logic from packages/config/src/network.js.
func IsInCIDR(ipStr, cidrStr string) bool {
	ipParts := strings.Split(ipStr, ".")
	if len(ipParts) != 4 {
		return false
	}
	var ipBytes [4]uint32
	for i, part := range ipParts {
		val, err := strconv.Atoi(part)
		if err != nil || val < 0 || val > 255 {
			return false
		}
		ipBytes[i] = uint32(val)
	}

	cidrParts := strings.Split(cidrStr, "/")
	if len(cidrParts) != 2 {
		return false
	}
	rangeParts := strings.Split(cidrParts[0], ".")
	if len(rangeParts) != 4 {
		return false
	}
	var rangeBytes [4]uint32
	for i, part := range rangeParts {
		val, err := strconv.Atoi(part)
		if err != nil || val < 0 || val > 255 {
			return false
		}
		rangeBytes[i] = uint32(val)
	}

	bits, err := strconv.Atoi(cidrParts[1])
	if err != nil || bits < 0 || bits > 32 {
		return false
	}

	ipInt := (ipBytes[0] << 24) | (ipBytes[1] << 16) | (ipBytes[2] << 8) | ipBytes[3]
	rangeInt := (rangeBytes[0] << 24) | (rangeBytes[1] << 16) | (rangeBytes[2] << 8) | rangeBytes[3]

	var maskInt uint32
	if bits == 0 {
		maskInt = 0
	} else {
		maskInt = ^uint32(0) << (32 - bits)
	}

	return (ipInt & maskInt) == (rangeInt & maskInt)
}

// GetDefaultWebhookHosts returns the standard outbound webhook host patterns from JavaScript.
func GetDefaultWebhookHosts() []string {
	return []string{
		"hooks.slack.com",
		"discord.com",
		"discordapp.com",
		"outlook.office.com",
		"webhook.office.com",
		"office.com",
		"office365.com",
		"*.office.com",
		"*.office365.com",
		"events.pagerduty.com",
		"events.eu.pagerduty.com",
		"*.pagerduty.com",
		"*.logic.azure.com",
		"*.environment.api.powerplatform.com",
	}
}

// IsWebhookAllowed validates whether a given webhook URL is permitted by host policy.
func IsWebhookAllowed(cfg *NetworkConfig, rawURL string) bool {
	if cfg != nil && cfg.WebhookAllowAllHosts {
		return true
	}

	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		return false
	}

	hostname := parsed.Hostname()
	// Strip enclosing IPv6 brackets if present
	if strings.HasPrefix(hostname, "[") && strings.HasSuffix(hostname, "]") {
		hostname = hostname[1 : len(hostname)-1]
	}

	allowedHosts := append(GetDefaultWebhookHosts(), cfg.WebhookProviderHosts...)
	allowedHosts = append(allowedHosts, cfg.WebhookExtraHosts...)

	for _, pattern := range allowedHosts {
		if MatchPattern(hostname, pattern) {
			return true
		}
	}
	return false
}

// IsLoopbackOrPrivateIP evaluates whether an IP address is loopback, link-local, or private.
func IsLoopbackOrPrivateIP(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsPrivate()
}
