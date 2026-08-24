package cursor

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestInstallKeepsAgentAndLinksOnlyCursorAgent(t *testing.T) {
	archive := packageArchive(t, map[string]string{
		"dist-package/cursor-agent": "#!/bin/sh\necho cursor\n",
		"dist-package/index.js":     "console.log('cursor')\n",
	})
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body []byte
		if request.URL.String() == installerURL {
			body = []byte(`DOWNLOAD_URL="https://downloads.cursor.com/lab/2026.08.11-test/${OS}/${ARCH}/agent-cli-package.tar.gz"`)
		} else {
			body = archive
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
	})}
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	agent := filepath.Join(bin, "agent")
	if err := os.WriteFile(agent, []byte("existing agent\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := Install(context.Background(), InstallOptions{Home: home, HTTPClient: client}, &output); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(agent)
	if err != nil || string(data) != "existing agent\n" {
		t.Fatalf("agent changed: data = %q, error = %v", data, err)
	}
	target, err := os.Readlink(filepath.Join(bin, "cursor-agent"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(target, filepath.Join("versions", "2026.08.11-test", "cursor-agent")) {
		t.Fatalf("cursor-agent target = %q", target)
	}
	if !strings.Contains(output.String(), "without changing ~/.local/bin/agent") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestInstallReplacesIncompleteVersionAndKeepsBinaryExecutable(t *testing.T) {
	client := packageClient(t, []archiveEntry{
		{name: "dist-package/cursor-agent", content: "#!/bin/sh\necho cursor\n", mode: 0o644},
		{name: "dist-package/node_modules/cursor", linkname: "../cursor-agent", typeflag: tar.TypeSymlink},
	})
	home := t.TempDir()
	incomplete := filepath.Join(home, ".local", "share", "cursor-agent", "versions", "2026.08.11-test")
	if err := os.MkdirAll(incomplete, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(incomplete, "cursor-agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	leftover := filepath.Join(incomplete, "partial.js")
	if err := os.WriteFile(leftover, []byte("partial\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Install(context.Background(), InstallOptions{Home: home, HTTPClient: client}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(leftover); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("incomplete installation was kept: %v", err)
	}
	link := filepath.Join(home, ".local", "bin", "cursor-agent")
	info, err := os.Stat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("cursor-agent is not executable: %v", info.Mode())
	}
	target, err := os.Readlink(filepath.Join(incomplete, "node_modules", "cursor"))
	if err != nil || target != filepath.Join("..", "cursor-agent") {
		t.Fatalf("packaged link target = %q, error = %v", target, err)
	}
	entries, err := os.ReadDir(filepath.Dir(incomplete))
	if err != nil || len(entries) != 1 {
		t.Fatalf("versions = %v, error = %v", entries, err)
	}
}

func TestLinkCursorAgentSupportsConcurrentCalls(t *testing.T) {
	home := t.TempDir()
	version := filepath.Join(home, ".local", "share", "cursor-agent", "versions", "test")
	if err := os.MkdirAll(version, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(version, "cursor-agent"), []byte("cursor\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	errorsFound := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsFound <- linkCursorAgent(home, version)
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	target, err := os.Readlink(filepath.Join(home, ".local", "bin", "cursor-agent"))
	if err != nil || target != filepath.Join(version, "cursor-agent") {
		t.Fatalf("target = %q, error = %v", target, err)
	}
}

func TestInstallKeepsAnExistingCompleteVersion(t *testing.T) {
	client := packageClient(t, []archiveEntry{{name: "dist-package/cursor-agent", content: "fresh\n"}})
	home := t.TempDir()
	versions := filepath.Join(home, ".local", "share", "cursor-agent", "versions")
	existing := filepath.Join(versions, "2026.08.11-test")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existing, "cursor-agent"), []byte("existing\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := Install(context.Background(), InstallOptions{Home: home, HTTPClient: client}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".local", "bin", "cursor-agent"))
	if err != nil || string(data) != "existing\n" {
		t.Fatalf("data = %q, error = %v", data, err)
	}
	entries, err := os.ReadDir(versions)
	if err != nil || len(entries) != 1 {
		t.Fatalf("versions = %v, error = %v", entries, err)
	}
}

func TestDownloadAndExtractKeepsAConcurrentCompleteInstall(t *testing.T) {
	client := packageClient(t, []archiveEntry{{name: "dist-package/cursor-agent", content: "fresh\n"}})
	versions := t.TempDir()
	final := filepath.Join(versions, "2026.08.11-test")
	if err := os.MkdirAll(final, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(final, "cursor-agent"), []byte("concurrent\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	packageURL := "https://downloads.cursor.com/lab/2026.08.11-test/darwin/arm64/agent-cli-package.tar.gz"
	if err := downloadAndExtract(context.Background(), client, packageURL, versions, final); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(final, "cursor-agent"))
	if err != nil || string(data) != "concurrent\n" {
		t.Fatalf("data = %q, error = %v", data, err)
	}
	entries, err := os.ReadDir(versions)
	if err != nil || len(entries) != 1 {
		t.Fatalf("versions = %v, error = %v", entries, err)
	}
}

func TestExtractPackageRejectsUnsafePath(t *testing.T) {
	archive := packageArchive(t, map[string]string{"dist-package/../../outside": "bad"})
	err := extractPackage(bytes.NewReader(archive), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Fatalf("error = %v", err)
	}
}

func TestExtractPackageRejectsEscapingLinks(t *testing.T) {
	for _, entry := range []archiveEntry{
		{name: "dist-package/escape", linkname: "../../outside", typeflag: tar.TypeSymlink},
		{name: "dist-package/escape", linkname: "/etc/passwd", typeflag: tar.TypeSymlink},
		{name: "dist-package/escape", linkname: "../../outside", typeflag: tar.TypeLink},
	} {
		destination := t.TempDir()
		err := extractPackage(bytes.NewReader(packageArchiveEntries(t, []archiveEntry{entry})), destination)
		if err == nil || !strings.Contains(err.Error(), "unsafe link") {
			t.Fatalf("%s %q error = %v", string(entry.typeflag), entry.linkname, err)
		}
		if _, statErr := os.Lstat(filepath.Join(destination, "escape")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("link was created: %v", statErr)
		}
	}
}

func TestResolvePackageRejectsUnsafeVersion(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		body := []byte(`DOWNLOAD_URL="https://downloads.cursor.com/lab/../../evil/${OS}/${ARCH}/agent-cli-package.tar.gz"`)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
	})}
	_, _, err := resolvePackage(context.Background(), client, "darwin", "arm64")
	if err == nil || !strings.Contains(err.Error(), "did not contain a supported package") {
		t.Fatalf("error = %v", err)
	}
}

type archiveEntry struct {
	name     string
	content  string
	linkname string
	mode     int64
	typeflag byte
}

func packageClient(t *testing.T, entries []archiveEntry) *http.Client {
	t.Helper()
	archive := packageArchiveEntries(t, entries)
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := archive
		if request.URL.String() == installerURL {
			body = []byte(`DOWNLOAD_URL="https://downloads.cursor.com/lab/2026.08.11-test/${OS}/${ARCH}/agent-cli-package.tar.gz"`)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
	})}
}

func packageArchiveEntries(t *testing.T, entries []archiveEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	compressed := gzip.NewWriter(&output)
	archive := tar.NewWriter(compressed)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		mode := entry.mode
		if mode == 0 {
			mode = 0o755
		}
		header := &tar.Header{Name: entry.name, Mode: mode, Typeflag: typeflag, Linkname: entry.linkname}
		if typeflag == tar.TypeReg {
			header.Size = int64(len(entry.content))
		}
		if err := archive.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if typeflag == tar.TypeReg {
			if _, err := archive.Write([]byte(entry.content)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func packageArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var output bytes.Buffer
	compressed := gzip.NewWriter(&output)
	archive := tar.NewWriter(compressed)
	for name, content := range files {
		if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
