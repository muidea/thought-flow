package main

import (
	"errors"
	"flag"
	"os"
	"testing"
)

func TestApplyStartupFlagEnvOverridesSelectedValues(t *testing.T) {
	t.Setenv("THOUGHTFLOW_HOST", "127.0.0.1")
	t.Setenv("THOUGHTFLOW_PORT", "8080")
	t.Setenv("THOUGHTFLOW_GIT_ENABLED", "true")

	err := applyStartupFlagEnv([]string{
		"--host", "0.0.0.0",
		"--port", "9090",
		"--workspace-root", "/tmp/thoughtflow",
		"--git-enabled", "false",
		"--ai-api-key", "cli-key",
	})
	if err != nil {
		t.Fatalf("applyStartupFlagEnv() error = %v", err)
	}

	assertEnv(t, "THOUGHTFLOW_HOST", "0.0.0.0")
	assertEnv(t, "THOUGHTFLOW_PORT", "9090")
	assertEnv(t, "THOUGHTFLOW_WORKSPACE_ROOT", "/tmp/thoughtflow")
	assertEnv(t, "THOUGHTFLOW_GIT_ENABLED", "false")
	assertEnv(t, "THOUGHTFLOW_AI_API_KEY", "cli-key")
}

func TestApplyStartupFlagEnvLeavesUnspecifiedValuesUntouched(t *testing.T) {
	t.Setenv("THOUGHTFLOW_PORT", "8080")

	if err := applyStartupFlagEnv([]string{"--host", "0.0.0.0"}); err != nil {
		t.Fatalf("applyStartupFlagEnv() error = %v", err)
	}

	assertEnv(t, "THOUGHTFLOW_HOST", "0.0.0.0")
	assertEnv(t, "THOUGHTFLOW_PORT", "8080")
}

func TestApplyStartupFlagEnvReturnsFlagErrors(t *testing.T) {
	if err := applyStartupFlagEnv([]string{"--unknown"}); err == nil {
		t.Fatal("expected unknown flag error")
	}
	if err := applyStartupFlagEnv([]string{"--help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help error = %v", err)
	}
}

func assertEnv(t *testing.T, key string, expected string) {
	t.Helper()
	if actual := os.Getenv(key); actual != expected {
		t.Fatalf("%s = %q, want %q", key, actual, expected)
	}
}
