package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/config"
)

func getFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestMain_VersionCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"wb", "version"}, nil, &stdout, &stderr, nil)
	if code != 0 {
		t.Fatalf("expected code 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "WhatBreaks Platform") {
		t.Errorf("expected version output, got: %s", stdout.String())
	}
}

func TestMain_VersionFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"wb", "--version"}, nil, &stdout, &stderr, nil)
	if code != 0 {
		t.Fatalf("expected code 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "WhatBreaks Platform") {
		t.Errorf("expected version output, got: %s", stdout.String())
	}
}

func TestMain_HelpFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"wb", "--help"}, nil, &stdout, &stderr, nil)
	if code != 0 {
		t.Fatalf("expected code 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "Usage: wb") {
		t.Errorf("expected usage output, got: %s", stdout.String())
	}
}

func TestMain_ForbiddenEnv(t *testing.T) {
	var stdout, stderr bytes.Buffer
	env := map[string]string{
		config.EnvWbApiToken: "secret-token",
	}
	getenv := func(k string) string { return env[k] }

	code := run([]string{"wb"}, getenv, &stdout, &stderr, nil)
	if code != 1 {
		t.Fatalf("expected code 1 on forbidden env, got %d", code)
	}
	if !strings.Contains(stderr.String(), "raw API tokens in environment variables are forbidden") {
		t.Errorf("expected forbidden token error message, got: %s", stderr.String())
	}

	// Test forbidden Kubeconfig env
	stdout.Reset()
	stderr.Reset()
	env = map[string]string{
		config.EnvKubeconfig: "/etc/kubernetes/admin.conf",
	}
	code = run([]string{"wb"}, getenv, &stdout, &stderr, nil)
	if code != 1 {
		t.Fatalf("expected code 1 on forbidden kubeconfig, got %d", code)
	}
	if !strings.Contains(stderr.String(), "KUBECONFIG environment variable is forbidden") {
		t.Errorf("expected forbidden kubeconfig error message, got: %s", stderr.String())
	}
}

func TestMain_StartupAndShutdown(t *testing.T) {
	apiPort := getFreePort(t)
	healthPort := getFreePort(t)

	var stdout, stderr bytes.Buffer
	env := map[string]string{
		"SESSION_SECRET": "0123456789abcdef0123456789abcdef",
		"NODE_ENV":       "test",
	}
	getenv := func(k string) string { return env[k] }

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan int, 1)
	go func() {
		args := []string{
			"wb",
			fmt.Sprintf("--api-port=%d", apiPort),
			fmt.Sprintf("--health-port=%d", healthPort),
		}
		code := run(args, getenv, &stdout, &stderr, ctx)
		done <- code
	}()

	// Give it a moment to boot
	time.Sleep(50 * time.Millisecond)

	// Cancel context to trigger shutdown
	cancel()

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("expected code 0, got %d; stderr: %s", code, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for main run to complete")
	}

	if !strings.Contains(stderr.String(), "WhatBreaks shutdown complete") {
		t.Errorf("expected shutdown complete message in stderr, got: %s", stderr.String())
	}
}
