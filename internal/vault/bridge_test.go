package vault

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestConnectArgumentsKeepSecretsOffTheCommandLine(t *testing.T) {
	arguments := connectArguments("dev:yg@melten.ai", "circlesac")
	if strings.Join(arguments, " ") != "--profile dev:yg@melten.ai --org circlesac connect --port 0" {
		t.Fatalf("arguments = %#v", arguments)
	}
	for _, argument := range arguments {
		if strings.Contains(argument, "token") || strings.Contains(argument, "secret") {
			t.Fatalf("command line contains credential-like value %q", argument)
		}
	}
}

func TestConnectExportRequiresAValidatedLoopbackBridge(t *testing.T) {
	host, found := connectExport("export OP_CONNECT_HOST=http://127.0.0.1:31415", "OP_CONNECT_HOST")
	if !found || !validLoopbackHost(host) {
		t.Fatalf("host = %q, found = %v", host, found)
	}
	for _, value := range []string{
		"https://127.0.0.1:31415",
		"http://vault.example.com:31415",
		"http://127.0.0.1:31415/path",
	} {
		if validLoopbackHost(value) {
			t.Fatalf("accepted non-loopback bridge host %q", value)
		}
	}
}

func TestStartConnectBridgeReadsExportsAndStopsTheChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake cvlt executable is a POSIX shell script")
	}
	directory := t.TempDir()
	argumentsFile := filepath.Join(directory, "arguments")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > \"$PRISM_TEST_CVLT_ARGUMENTS\"\n" +
		"printf '%s\\n' '1Password Connect bridge listening'\n" +
		"printf '%s\\n' 'export OP_CONNECT_HOST=http://127.0.0.1:31415'\n" +
		"printf '%s\\n' 'export OP_CONNECT_TOKEN=local-test-token'\n" +
		"while :; do sleep 1; done\n"
	if err := os.WriteFile(filepath.Join(directory, "cvlt"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PRISM_TEST_CVLT_ARGUMENTS", argumentsFile)

	bridge, err := StartConnectBridge(
		context.Background(),
		"prod:yg@example.com",
		"circlesac",
		strings.NewReader(""),
		io.Discard,
	)
	if err != nil {
		t.Fatal(err)
	}
	if bridge.Host != "http://127.0.0.1:31415" || bridge.Token != "local-test-token" {
		t.Fatalf("bridge = %#v", bridge)
	}
	if err := bridge.Close(); err != nil {
		t.Fatal(err)
	}
	arguments, err := os.ReadFile(argumentsFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(arguments) != "--profile\nprod:yg@example.com\n--org\ncirclesac\nconnect\n--port\n0\n" {
		t.Fatalf("cvlt arguments = %q", arguments)
	}
}
