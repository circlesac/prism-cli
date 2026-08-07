package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCodexEnableDisablePreservesUnrelatedConfiguration(t *testing.T) {
	configPath, statePath, crclPath := prepareCodexTest(t)
	original := `# keep this comment
model = "gpt-5.5"
model_reasoning_effort = "high"
approval_policy = "never"

[projects."/tmp/example"]
trust_level = "trusted"
`
	if err := os.WriteFile(configPath, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"codex", "enable"}, &stdout, &bytes.Buffer{}, "test"); err != nil {
		t.Fatal(err)
	}
	enabled := readTestFile(t, configPath)
	for _, expected := range []string{
		`model = "gpt-5.6-sol"`,
		`model_provider = "prism"`,
		`[model_providers.prism]`,
		`base_url = "https://prism.circles.ac/v1"`,
		`[model_providers.prism.auth]`,
		`command = ` + quotedString(t, crclPath),
		`args = ["auth", "token"]`,
		`[projects."/tmp/example"]`,
	} {
		if !strings.Contains(enabled, expected) {
			t.Fatalf("enabled config does not contain %q:\n%s", expected, enabled)
		}
	}
	if strings.Contains(enabled, "model_reasoning_effort") {
		t.Fatalf("enabled config sets reasoning effort:\n%s", enabled)
	}
	if info, err := os.Stat(configPath); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("config mode = %v, %v", info, err)
	}

	generated := `
[projects."/tmp/generated"]
trust_level = "trusted"
`
	if err := os.WriteFile(configPath, []byte(enabled+generated), 0o640); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"codex", "disable"}, &stdout, &bytes.Buffer{}, "test"); err != nil {
		t.Fatal(err)
	}
	restored := readTestFile(t, configPath)
	for _, expected := range []string{
		`model = "gpt-5.5"`,
		`model_reasoning_effort = "high"`,
		`approval_policy = "never"`,
		`[projects."/tmp/example"]`,
		`[projects."/tmp/generated"]`,
	} {
		if !strings.Contains(restored, expected) {
			t.Fatalf("restored config does not contain %q:\n%s", expected, restored)
		}
	}
	for _, unexpected := range []string{
		`model = "gpt-5.6-sol"`,
		`model_provider = "prism"`,
		`[model_providers.prism]`,
	} {
		if strings.Contains(restored, unexpected) {
			t.Fatalf("restored config still contains %q:\n%s", unexpected, restored)
		}
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("state file still exists: %v", err)
	}
}

func TestCodexCommandsAreIdempotentAndReportStatus(t *testing.T) {
	configPath, _, _ := prepareCodexTest(t)
	if err := os.WriteFile(configPath, []byte("approval_policy = \"never\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"codex", "status"}, &stdout, &bytes.Buffer{}, "test"); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "Codex: direct\n" {
		t.Fatalf("direct status = %q", stdout.String())
	}

	stdout.Reset()
	if err := Run(context.Background(), []string{"codex", "enable"}, &stdout, &bytes.Buffer{}, "test"); err != nil {
		t.Fatal(err)
	}
	enabled := readTestFile(t, configPath)
	stdout.Reset()
	if err := Run(context.Background(), []string{"codex", "enable"}, &stdout, &bytes.Buffer{}, "test"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "already enabled") || readTestFile(t, configPath) != enabled {
		t.Fatalf("second enable = %q", stdout.String())
	}

	stdout.Reset()
	if err := Run(context.Background(), []string{"codex", "status"}, &stdout, &bytes.Buffer{}, "test"); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "Codex: prism\n" {
		t.Fatalf("prism status = %q", stdout.String())
	}

	stdout.Reset()
	if err := Run(context.Background(), []string{"codex", "disable"}, &stdout, &bytes.Buffer{}, "test"); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"codex", "disable"}, &stdout, &bytes.Buffer{}, "test"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "already disabled") {
		t.Fatalf("second disable = %q", stdout.String())
	}
}

func TestCodexDisableRemovesConfigurationCreatedByEnable(t *testing.T) {
	configPath, _, _ := prepareCodexTest(t)
	if err := Run(context.Background(), []string{"codex", "enable"}, &bytes.Buffer{}, &bytes.Buffer{}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), []string{"codex", "disable"}, &bytes.Buffer{}, &bytes.Buffer{}, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("configuration created by enable still exists: %v", err)
	}
}

func TestCodexStatusRecognizesUnmanagedPrismConfiguration(t *testing.T) {
	configPath, statePath, _ := prepareCodexTest(t)
	if err := Run(context.Background(), []string{"codex", "enable"}, &bytes.Buffer{}, &bytes.Buffer{}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"codex", "status"}, &stdout, &bytes.Buffer{}, "test"); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "Codex: prism (not managed by Prism CLI)\n" {
		t.Fatalf("unmanaged status = %q; config:\n%s", stdout.String(), readTestFile(t, configPath))
	}
}

func TestCodexDisableRefusesManagedSettingDrift(t *testing.T) {
	configPath, statePath, _ := prepareCodexTest(t)
	if err := os.WriteFile(configPath, []byte("model = \"gpt-5.5\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), []string{"codex", "enable"}, &bytes.Buffer{}, &bytes.Buffer{}, "test"); err != nil {
		t.Fatal(err)
	}
	drifted := strings.Replace(readTestFile(t, configPath), `model = "gpt-5.6-sol"`, `model = "gpt-5.6-terra"`, 1)
	if err := os.WriteFile(configPath, []byte(drifted), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"codex", "status"}, &stdout, &bytes.Buffer{}, "test"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Codex: drift") || !strings.Contains(stdout.String(), "model") {
		t.Fatalf("drift status = %q", stdout.String())
	}
	err := Run(context.Background(), []string{"codex", "disable"}, &bytes.Buffer{}, &bytes.Buffer{}, "test")
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite: model") {
		t.Fatalf("disable error = %v", err)
	}
	if readTestFile(t, configPath) != drifted {
		t.Fatal("drifted config was modified")
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("state file was removed: %v", err)
	}
}

func TestCodexEnableAdoptsExistingPrismConfiguration(t *testing.T) {
	configPath, statePath, _ := prepareCodexTest(t)
	if err := Run(context.Background(), []string{"codex", "enable"}, &bytes.Buffer{}, &bytes.Buffer{}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	manual := strings.Replace(readTestFile(t, configPath),
		`command = `+quotedString(t, filepath.Join(filepath.Dir(filepath.Dir(configPath)), "bin", "crcl"))+"\n"+`args = ["auth", "token"]`,
		`command = "/tmp/example-codex-token"`,
		1,
	)
	if err := os.WriteFile(configPath, []byte(manual), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), []string{"codex", "enable"}, &bytes.Buffer{}, &bytes.Buffer{}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), []string{"codex", "disable"}, &bytes.Buffer{}, &bytes.Buffer{}, "test"); err != nil {
		t.Fatal(err)
	}
	restored := readTestFile(t, configPath)
	if !strings.Contains(restored, `model = "gpt-5.6-sol"`) {
		t.Fatalf("model was not preserved:\n%s", restored)
	}
	provider, err := parseCodexDocument([]byte(restored)).value(codexUnit{kind: codexAssignment, name: "model_provider"})
	if err != nil {
		t.Fatal(err)
	}
	if provider.Present {
		t.Fatalf("Prism provider selection was not removed:\n%s", restored)
	}
	if !strings.Contains(restored, `command = "/tmp/example-codex-token"`) {
		t.Fatalf("existing Prism provider definition was not preserved:\n%s", restored)
	}
}

func TestCodexConfigSymlinkIsPreserved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires additional privileges on Windows")
	}
	temporary := t.TempDir()
	codexHome := filepath.Join(temporary, "codex-home")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(temporary, "managed-config.toml")
	if err := os.WriteFile(target, []byte("approval_policy = \"never\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(codexHome, "config.toml")
	if err := os.Symlink(target, configPath); err != nil {
		t.Fatal(err)
	}
	prepareCodexEnvironment(t, temporary, codexHome)

	if err := Run(context.Background(), []string{"codex", "enable"}, &bytes.Buffer{}, &bytes.Buffer{}, "test"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(configPath); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("config symlink was replaced: %v, %v", info, err)
	}
	if !strings.Contains(readTestFile(t, target), `model_provider = "prism"`) {
		t.Fatal("symlink target was not updated")
	}
	if err := Run(context.Background(), []string{"codex", "disable"}, &bytes.Buffer{}, &bytes.Buffer{}, "test"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(configPath); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("config symlink was replaced during restore: %v, %v", info, err)
	}
}

func TestCodexHelpDoesNotRequireCrcl(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"codex", "--help"}, &stdout, &bytes.Buffer{}, "test"); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"prism codex enable", "prism codex disable", "prism codex status", "Codex App and CLI"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("help does not contain %q:\n%s", expected, stdout.String())
		}
	}
}

func prepareCodexTest(t *testing.T) (string, string, string) {
	t.Helper()
	temporary := t.TempDir()
	codexHome := filepath.Join(temporary, "codex-home")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	crclPath := prepareCodexEnvironment(t, temporary, codexHome)
	return filepath.Join(codexHome, "config.toml"), filepath.Join(temporary, "config", "prism", "codex-state.json"), crclPath
}

func prepareCodexEnvironment(t *testing.T, temporary string, codexHome string) string {
	t.Helper()
	binDirectory := filepath.Join(temporary, "bin")
	if err := os.MkdirAll(binDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	crclPath := filepath.Join(binDirectory, "crcl")
	if err := os.WriteFile(crclPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(temporary, "config"))
	t.Setenv("PATH", binDirectory)
	return crclPath
}

func quotedString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
