package vault

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const connectStartupTimeout = 30 * time.Second

// ConnectBridge is a localhost-only cvlt bridge. Provider credentials and
// Vault encryption keys stay in cvlt; Prism sends plaintext only over the
// authenticated loopback connection for the lifetime of one command.
type ConnectBridge struct {
	Host  string
	Token string

	command   *exec.Cmd
	closeOnce sync.Once
}

type bridgeReady struct {
	host  string
	token string
	err   error
}

func connectArguments(profile string, org string) []string {
	arguments := make([]string, 0, 7)
	if profile != "" {
		arguments = append(arguments, "--profile", profile)
	}
	if org != "" {
		arguments = append(arguments, "--org", org)
	}
	return append(arguments, "connect", "--port", "0")
}

func connectExport(line string, name string) (string, bool) {
	value, found := strings.CutPrefix(strings.TrimSpace(line), "export "+name+"=")
	if !found || value == "" || strings.ContainsAny(value, " \t\r\n") {
		return "", false
	}
	return value, true
}

func validLoopbackHost(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Port() == "" {
		return false
	}
	if parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "127.0.0.1", "::1", "localhost":
		return true
	default:
		return false
	}
}

// StartConnectBridge starts the installed cvlt binary without placing either
// the Circles credential or provider credential on the command line.
func StartConnectBridge(
	ctx context.Context,
	profile string,
	org string,
	stdin io.Reader,
	stderr io.Writer,
) (*ConnectBridge, error) {
	executable, err := exec.LookPath("cvlt")
	if err != nil {
		return nil, errors.New("cvlt is required to access encrypted Circles Vault content; install circlesac/tap/cvlt")
	}
	command := exec.CommandContext(ctx, executable, connectArguments(profile, org)...)
	command.Stdin = stdin
	if stderr != nil {
		command.Stderr = stderr
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, errors.New("could not capture cvlt bridge output")
	}
	if err := command.Start(); err != nil {
		return nil, errors.New("could not start the cvlt bridge")
	}

	ready := make(chan bridgeReady, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		var host string
		var token string
		sent := false
		for scanner.Scan() {
			line := scanner.Text()
			if value, found := connectExport(line, "OP_CONNECT_HOST"); found {
				host = value
			}
			if value, found := connectExport(line, "OP_CONNECT_TOKEN"); found {
				token = value
			}
			if !sent && host != "" && token != "" {
				if !validLoopbackHost(host) {
					ready <- bridgeReady{err: errors.New("cvlt bridge reported a non-loopback address")}
				} else {
					ready <- bridgeReady{host: host, token: token}
				}
				sent = true
			}
		}
		if !sent {
			if scanner.Err() != nil {
				ready <- bridgeReady{err: errors.New("could not read cvlt bridge output")}
			} else {
				ready <- bridgeReady{err: errors.New("cvlt exited before its encrypted Vault bridge was ready")}
			}
		}
	}()

	timer := time.NewTimer(connectStartupTimeout)
	defer timer.Stop()
	select {
	case result := <-ready:
		if result.err != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
			return nil, result.err
		}
		return &ConnectBridge{Host: result.host, Token: result.token, command: command}, nil
	case <-ctx.Done():
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, ctx.Err()
	case <-timer.C:
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, fmt.Errorf("cvlt bridge did not start within %s", connectStartupTimeout)
	}
}

func (bridge *ConnectBridge) Close() error {
	if bridge == nil || bridge.command == nil {
		return nil
	}
	bridge.closeOnce.Do(func() {
		if bridge.command.Process != nil {
			_ = bridge.command.Process.Kill()
		}
		_ = bridge.command.Wait()
	})
	return nil
}
