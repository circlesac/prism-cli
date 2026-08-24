package cursor

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const installerURL = "https://cursor.com/install"

var packageURLPattern = regexp.MustCompile(`https://downloads\.cursor\.com/lab/([A-Za-z0-9][A-Za-z0-9._+-]*)/\$\{OS\}/\$\{ARCH\}/agent-cli-package\.tar\.gz`)

type InstallOptions struct {
	Home       string
	HTTPClient *http.Client
}

func Install(ctx context.Context, options InstallOptions, output io.Writer) error {
	home := options.Home
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return errors.New("could not find the home directory")
		}
	}
	osName, architecture, err := platform()
	if err != nil {
		return err
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	version, packageURL, err := resolvePackage(ctx, client, osName, architecture)
	if err != nil {
		return err
	}
	versionsDirectory := filepath.Join(home, ".local", "share", "cursor-agent", "versions")
	finalDirectory := filepath.Join(versionsDirectory, version)
	binary := filepath.Join(finalDirectory, "cursor-agent")
	ready, err := regularFile(binary)
	if err != nil {
		return fmt.Errorf("could not inspect the Cursor Agent installation: %w", err)
	}
	if !ready {
		if err := downloadAndExtract(ctx, client, packageURL, versionsDirectory, finalDirectory); err != nil {
			return err
		}
		ready, err = regularFile(binary)
		if err != nil || !ready {
			return errors.New("Cursor Agent installation is incomplete; run 'prism cursor install' again")
		}
	}
	if err := makeExecutable(binary); err != nil {
		return err
	}
	if err := linkCursorAgent(home, finalDirectory); err != nil {
		return err
	}
	fmt.Fprintf(output, "Installed Cursor Agent %s without changing ~/.local/bin/agent.\n", version)
	return nil
}

func platform() (string, string, error) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return "", "", fmt.Errorf("Cursor Agent does not support %s", runtime.GOOS)
	}
	switch runtime.GOARCH {
	case "arm64":
		return runtime.GOOS, "arm64", nil
	case "amd64":
		return runtime.GOOS, "x64", nil
	default:
		return "", "", fmt.Errorf("Cursor Agent does not support %s/%s", runtime.GOOS, runtime.GOARCH)
	}
}

func resolvePackage(ctx context.Context, client *http.Client, osName string, architecture string) (string, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, installerURL, nil)
	if err != nil {
		return "", "", errors.New("could not create the Cursor installer request")
	}
	response, err := client.Do(request)
	if err != nil {
		return "", "", errors.New("could not download the Cursor installer metadata")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("Cursor installer metadata returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", "", errors.New("could not read the Cursor installer metadata")
	}
	match := packageURLPattern.FindSubmatch(data)
	if len(match) != 2 {
		return "", "", errors.New("Cursor installer metadata did not contain a supported package")
	}
	version := string(match[1])
	return version, fmt.Sprintf(
		"https://downloads.cursor.com/lab/%s/%s/%s/agent-cli-package.tar.gz",
		version,
		osName,
		architecture,
	), nil
}

func downloadAndExtract(
	ctx context.Context,
	client *http.Client,
	packageURL string,
	versionsDirectory string,
	finalDirectory string,
) error {
	if err := os.MkdirAll(versionsDirectory, 0o755); err != nil {
		return fmt.Errorf("could not create the Cursor Agent versions directory: %w", err)
	}
	temporaryDirectory, err := os.MkdirTemp(versionsDirectory, ".install-")
	if err != nil {
		return fmt.Errorf("could not create a temporary Cursor Agent directory: %w", err)
	}
	defer os.RemoveAll(temporaryDirectory)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, packageURL, nil)
	if err != nil {
		return errors.New("could not create the Cursor Agent package request")
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("could not download the Cursor Agent package")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Cursor Agent package returned HTTP %d", response.StatusCode)
	}
	if err := extractPackage(response.Body, temporaryDirectory); err != nil {
		return err
	}
	ready, err := regularFile(filepath.Join(temporaryDirectory, "cursor-agent"))
	if err != nil || !ready {
		return errors.New("Cursor Agent package did not contain cursor-agent")
	}
	if err := os.Rename(temporaryDirectory, finalDirectory); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("could not finalize the Cursor Agent installation: %w", err)
		}
		ready, statErr := regularFile(filepath.Join(finalDirectory, "cursor-agent"))
		if statErr == nil && ready {
			return nil
		}
		return replaceInstallation(versionsDirectory, temporaryDirectory, finalDirectory)
	}
	return nil
}

func replaceInstallation(versionsDirectory string, temporaryDirectory string, finalDirectory string) error {
	aside, err := os.MkdirTemp(versionsDirectory, ".replaced-")
	if err != nil {
		return fmt.Errorf("could not replace the incomplete Cursor Agent installation: %w", err)
	}
	removeAside := true
	defer func() {
		if removeAside {
			_ = os.RemoveAll(aside)
		}
	}()
	replaced := filepath.Join(aside, "cursor-agent-version")
	replacedExisting := false
	if err := os.Rename(finalDirectory, replaced); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("could not replace the incomplete Cursor Agent installation: %w", err)
	} else if err == nil {
		replacedExisting = true
	}
	if err := os.Rename(temporaryDirectory, finalDirectory); err != nil {
		if replacedExisting {
			if restoreErr := os.Rename(replaced, finalDirectory); restoreErr != nil {
				removeAside = false
				return fmt.Errorf("could not finalize the Cursor Agent installation: %w; previous installation remains at %s", err, replaced)
			}
		}
		return fmt.Errorf("could not finalize the Cursor Agent installation: %w", err)
	}
	return nil
}

func extractPackage(input io.Reader, destination string) error {
	compressed, err := gzip.NewReader(input)
	if err != nil {
		return errors.New("Cursor Agent package is not a valid gzip archive")
	}
	defer compressed.Close()
	archive := tar.NewReader(io.LimitReader(compressed, 2<<30))
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return errors.New("Cursor Agent package is not a valid tar archive")
		}
		cleanName, ok := packageEntryPath(header.Name)
		if !ok {
			continue
		}
		if cleanName == "" {
			return errors.New("Cursor Agent package contained an unsafe path")
		}
		path := filepath.Join(destination, cleanName)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o755); err != nil {
				return fmt.Errorf("could not extract the Cursor Agent package: %w", err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > 1<<30 {
				return errors.New("Cursor Agent package contained an invalid file size")
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return fmt.Errorf("could not extract the Cursor Agent package: %w", err)
			}
			file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, header.FileInfo().Mode().Perm()&0o755|0o600)
			if err != nil {
				return fmt.Errorf("could not extract the Cursor Agent package: %w", err)
			}
			_, copyErr := io.CopyN(file, archive, header.Size)
			closeErr := file.Close()
			if copyErr != nil || closeErr != nil {
				return errors.New("could not write a Cursor Agent package file")
			}
		case tar.TypeSymlink:
			target, ok := containedLinkTarget(cleanName, header.Linkname)
			if !ok {
				return errors.New("Cursor Agent package contained an unsafe link")
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return fmt.Errorf("could not extract the Cursor Agent package: %w", err)
			}
			if err := os.Symlink(target, path); err != nil {
				return fmt.Errorf("could not extract the Cursor Agent package: %w", err)
			}
		case tar.TypeLink:
			target, ok := packageEntryPath(header.Linkname)
			if !ok || target == "" {
				return errors.New("Cursor Agent package contained an unsafe link")
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return fmt.Errorf("could not extract the Cursor Agent package: %w", err)
			}
			if err := os.Link(filepath.Join(destination, target), path); err != nil {
				return fmt.Errorf("could not extract the Cursor Agent package: %w", err)
			}
		default:
			return errors.New("Cursor Agent package contained an unsupported entry")
		}
	}
	return nil
}

func packageEntryPath(name string) (string, bool) {
	trimmed, ok := strings.CutPrefix(filepath.ToSlash(name), "dist-package/")
	if !ok || trimmed == "" {
		return "", false
	}
	cleanName := filepath.Clean(filepath.FromSlash(trimmed))
	if cleanName == "." || cleanName == ".." || filepath.IsAbs(cleanName) ||
		strings.HasPrefix(cleanName, ".."+string(filepath.Separator)) {
		return "", true
	}
	return cleanName, true
}

func containedLinkTarget(entryName string, linkName string) (string, bool) {
	target := filepath.FromSlash(linkName)
	if target == "" || filepath.IsAbs(target) || strings.HasPrefix(linkName, "/") {
		return "", false
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(entryName), target))
	if resolved == ".." || strings.HasPrefix(resolved, ".."+string(filepath.Separator)) {
		return "", false
	}
	return target, true
}

func makeExecutable(binary string) error {
	info, err := os.Stat(binary)
	if err != nil {
		return fmt.Errorf("could not inspect the Cursor Agent binary: %w", err)
	}
	mode := info.Mode().Perm()
	if mode&0o100 != 0 {
		return nil
	}
	if err := os.Chmod(binary, mode|0o700); err != nil {
		return fmt.Errorf("could not make the Cursor Agent binary executable: %w", err)
	}
	return nil
}

func regularFile(path string) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Mode().IsRegular(), nil
}

func linkCursorAgent(home string, versionDirectory string) error {
	binDirectory := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDirectory, 0o755); err != nil {
		return fmt.Errorf("could not create ~/.local/bin: %w", err)
	}
	target := filepath.Join(versionDirectory, "cursor-agent")
	link := filepath.Join(binDirectory, "cursor-agent")
	temporaryDirectory, err := os.MkdirTemp(binDirectory, ".cursor-agent.prism-")
	if err != nil {
		return fmt.Errorf("could not create a temporary Cursor Agent link directory: %w", err)
	}
	defer os.RemoveAll(temporaryDirectory)
	temporaryLink := filepath.Join(temporaryDirectory, "cursor-agent")
	if err := os.Symlink(target, temporaryLink); err != nil {
		return fmt.Errorf("could not create the Cursor Agent link: %w", err)
	}
	if err := os.Rename(temporaryLink, link); err != nil {
		return fmt.Errorf("could not replace the Cursor Agent link: %w", err)
	}
	return nil
}
