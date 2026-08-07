package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const codexStateVersion = 1

type codexPaths struct {
	configPath         string
	resolvedConfigPath string
	statePath          string
}

type codexManagedValue struct {
	Present bool   `json:"present"`
	Text    string `json:"text,omitempty"`
}

type codexManagedEntry struct {
	Previous codexManagedValue `json:"previous"`
	Applied  codexManagedValue `json:"applied"`
}

type codexState struct {
	Version            int                          `json:"version"`
	ConfigPath         string                       `json:"config_path"`
	ResolvedConfigPath string                       `json:"resolved_config_path"`
	ConfigExisted      bool                         `json:"config_existed"`
	Entries            map[string]codexManagedEntry `json:"entries"`
}

type codexUnitKind int

const (
	codexAssignment codexUnitKind = iota
	codexTable
)

type codexUnit struct {
	id      string
	kind    codexUnitKind
	name    string
	applied codexManagedValue
}

type codexDocument struct {
	lines   []string
	newline string
}

func runCodexCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h")) {
		printCodexHelp(stdout)
		return nil
	}
	if len(args) != 1 {
		return errors.New("usage: prism codex enable|disable|status")
	}
	paths, err := resolveCodexPaths()
	if err != nil {
		return err
	}
	switch args[0] {
	case "enable":
		return enableCodex(paths, stdout)
	case "disable":
		return disableCodex(paths, stdout)
	case "status":
		return printCodexStatus(paths, stdout)
	default:
		return errors.New("unknown Codex command; use enable, disable, or status")
	}
}

func enableCodex(paths codexPaths, stdout io.Writer) error {
	crclPath, err := exec.LookPath("crcl")
	if err != nil {
		return errors.New("crcl is not installed or is not on PATH")
	}
	crclPath, err = filepath.Abs(crclPath)
	if err != nil {
		return errors.New("could not resolve the crcl path")
	}
	units := codexUnits(crclPath)
	configData, configExisted, configMode, err := readOptionalFile(paths.resolvedConfigPath)
	if err != nil {
		return fmt.Errorf("could not read Codex configuration: %w", err)
	}
	document := parseCodexDocument(configData)
	state, stateExists, err := readCodexState(paths.statePath)
	if err != nil {
		return err
	}
	if stateExists {
		if err := validateCodexState(state, paths, units); err != nil {
			return err
		}
		conflicts, err := codexConflicts(document, state, false)
		if err != nil {
			return err
		}
		if len(conflicts) == 0 {
			fmt.Fprintln(stdout, "Prism is already enabled for Codex.")
			return nil
		}
		matchesPrevious, err := codexMatchesPrevious(document, state)
		if err != nil {
			return err
		}
		if !matchesPrevious {
			return codexDriftError(conflicts)
		}
		if err := os.Remove(paths.statePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("could not remove stale Codex state: %w", err)
		}
	}

	entries := make(map[string]codexManagedEntry, len(units))
	for _, unit := range units {
		current, err := document.value(unit)
		if err != nil {
			return err
		}
		entries[unit.id] = codexManagedEntry{Previous: current, Applied: unit.applied}
	}
	providerEntry := entries["model_provider"]
	if codexProviderIsPrism(providerEntry.Previous) {
		providerEntry.Previous = codexManagedValue{}
		entries["model_provider"] = providerEntry
	}
	for _, unit := range units {
		if err := document.set(unit, entries[unit.id].Applied); err != nil {
			return err
		}
	}
	state = codexState{
		Version:            codexStateVersion,
		ConfigPath:         paths.configPath,
		ResolvedConfigPath: paths.resolvedConfigPath,
		ConfigExisted:      configExisted,
		Entries:            entries,
	}
	if err := writeCodexState(paths.statePath, state); err != nil {
		return err
	}
	if err := replaceFileIfUnchanged(
		paths.resolvedConfigPath,
		configData,
		configExisted,
		document.bytes(),
		true,
		configMode,
	); err != nil {
		_ = os.Remove(paths.statePath)
		return fmt.Errorf("could not update Codex configuration: %w", err)
	}
	fmt.Fprintln(stdout, "Enabled Prism for Codex. Open a new Codex task to use gpt-5.6-sol.")
	return nil
}

func disableCodex(paths codexPaths, stdout io.Writer) error {
	state, stateExists, err := readCodexState(paths.statePath)
	if err != nil {
		return err
	}
	if !stateExists {
		mode, _, err := codexModeWithoutState(paths.resolvedConfigPath)
		if err != nil {
			return err
		}
		switch mode {
		case "direct":
			fmt.Fprintln(stdout, "Prism is already disabled for Codex.")
			return nil
		case "prism":
			return errors.New("Codex uses Prism, but Prism CLI has no saved configuration to restore; run 'prism codex enable' first")
		default:
			return errors.New("Codex configuration has Prism-related drift; run 'prism codex status'")
		}
	}
	units := codexUnits("")
	if err := validateCodexState(state, paths, units); err != nil {
		return err
	}
	configData, configExisted, configMode, err := readOptionalFile(paths.resolvedConfigPath)
	if err != nil {
		return fmt.Errorf("could not read Codex configuration: %w", err)
	}
	document := parseCodexDocument(configData)
	conflicts, err := codexConflicts(document, state, true)
	if err != nil {
		return err
	}
	if len(conflicts) != 0 {
		return codexDriftError(conflicts)
	}
	for _, unit := range units {
		entry := state.Entries[unit.id]
		current, err := document.value(unit)
		if err != nil {
			return err
		}
		if current == entry.Applied {
			if err := document.set(unit, entry.Previous); err != nil {
				return err
			}
		}
	}
	updated := document.bytes()
	keepFile := state.ConfigExisted || strings.TrimSpace(string(updated)) != ""
	if err := replaceFileIfUnchanged(
		paths.resolvedConfigPath,
		configData,
		configExisted,
		updated,
		keepFile,
		configMode,
	); err != nil {
		return fmt.Errorf("could not restore Codex configuration: %w", err)
	}
	if err := os.Remove(paths.statePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("Codex configuration was restored, but saved Prism state could not be removed: %w", err)
	}
	fmt.Fprintln(stdout, "Disabled Prism for Codex. Open a new Codex task to use the restored settings.")
	return nil
}

func printCodexStatus(paths codexPaths, stdout io.Writer) error {
	state, stateExists, err := readCodexState(paths.statePath)
	if err != nil {
		return err
	}
	if !stateExists {
		mode, conflicts, err := codexModeWithoutState(paths.resolvedConfigPath)
		if err != nil {
			return err
		}
		if mode == "prism" {
			fmt.Fprintln(stdout, "Codex: prism (not managed by Prism CLI)")
			return nil
		}
		fmt.Fprintf(stdout, "Codex: %s\n", mode)
		if len(conflicts) != 0 {
			fmt.Fprintf(stdout, "Conflicting settings: %s\n", strings.Join(conflicts, ", "))
		}
		return nil
	}
	units := codexUnits("")
	if err := validateCodexState(state, paths, units); err != nil {
		fmt.Fprintln(stdout, "Codex: drift")
		fmt.Fprintf(stdout, "Conflict: %s\n", err)
		return nil
	}
	configData, _, _, err := readOptionalFile(paths.resolvedConfigPath)
	if err != nil {
		return fmt.Errorf("could not read Codex configuration: %w", err)
	}
	document := parseCodexDocument(configData)
	conflicts, err := codexConflicts(document, state, false)
	if err != nil {
		return err
	}
	if len(conflicts) == 0 {
		fmt.Fprintln(stdout, "Codex: prism")
		return nil
	}
	matchesPrevious, err := codexMatchesPrevious(document, state)
	if err != nil {
		return err
	}
	if matchesPrevious {
		fmt.Fprintln(stdout, "Codex: direct (saved Prism state is stale)")
		return nil
	}
	fmt.Fprintln(stdout, "Codex: drift")
	fmt.Fprintf(stdout, "Conflicting settings: %s\n", strings.Join(conflicts, ", "))
	return nil
}

func codexUnits(crclPath string) []codexUnit {
	quotedCrclPath, _ := json.Marshal(crclPath)
	return []codexUnit{
		{id: "model", kind: codexAssignment, name: "model", applied: codexPresentValue(`model = "gpt-5.6-sol"`)},
		{id: "model_provider", kind: codexAssignment, name: "model_provider", applied: codexPresentValue(`model_provider = "prism"`)},
		{id: "model_reasoning_effort", kind: codexAssignment, name: "model_reasoning_effort"},
		{
			id:   "model_providers.prism",
			kind: codexTable,
			name: "model_providers.prism",
			applied: codexPresentValue(`[model_providers.prism]
name = "Prism"
base_url = "https://prism.circles.ac/v1"
wire_api = "responses"`),
		},
		{
			id:   "model_providers.prism.auth",
			kind: codexTable,
			name: "model_providers.prism.auth",
			applied: codexPresentValue(fmt.Sprintf(`[model_providers.prism.auth]
command = %s
args = ["auth", "token"]`, quotedCrclPath)),
		},
	}
}

func codexPresentValue(text string) codexManagedValue {
	return codexManagedValue{Present: true, Text: text}
}

func resolveCodexPaths() (codexPaths, error) {
	configHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return codexPaths{}, errors.New("could not find the home directory")
		}
		configHome = filepath.Join(home, ".codex")
	}
	configPath, err := filepath.Abs(filepath.Join(configHome, "config.toml"))
	if err != nil {
		return codexPaths{}, errors.New("could not resolve the Codex configuration path")
	}
	resolvedConfigPath, err := resolveFilePath(configPath)
	if err != nil {
		return codexPaths{}, fmt.Errorf("could not resolve the Codex configuration path: %w", err)
	}
	stateHome := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if stateHome == "" {
		if runtime.GOOS == "windows" {
			stateHome, err = os.UserConfigDir()
			if err != nil {
				return codexPaths{}, errors.New("could not find the user configuration directory")
			}
		} else {
			home, homeErr := os.UserHomeDir()
			if homeErr != nil {
				return codexPaths{}, errors.New("could not find the home directory")
			}
			stateHome = filepath.Join(home, ".config")
		}
	}
	statePath, err := filepath.Abs(filepath.Join(stateHome, "prism", "codex-state.json"))
	if err != nil {
		return codexPaths{}, errors.New("could not resolve the Prism configuration path")
	}
	return codexPaths{
		configPath:         configPath,
		resolvedConfigPath: resolvedConfigPath,
		statePath:          statePath,
	}, nil
}

func resolveFilePath(path string) (string, error) {
	if _, err := os.Lstat(path); err == nil {
		return filepath.EvalSymlinks(path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	directory, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		directory = filepath.Dir(path)
	}
	return filepath.Join(directory, filepath.Base(path)), nil
}

func readOptionalFile(path string) ([]byte, bool, fs.FileMode, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, 0o600, nil
	}
	if err != nil {
		return nil, false, 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, false, 0, err
	}
	return data, true, info.Mode().Perm(), nil
}

func readCodexState(path string) (codexState, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return codexState{}, false, nil
	}
	if err != nil {
		return codexState{}, false, fmt.Errorf("could not read saved Codex state: %w", err)
	}
	var state codexState
	if err := json.Unmarshal(data, &state); err != nil {
		return codexState{}, false, errors.New("saved Codex state is invalid")
	}
	return state, true, nil
}

func writeCodexState(path string, state codexState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return errors.New("could not encode Codex state")
	}
	data = append(data, '\n')
	if err := writeFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("could not save Codex state: %w", err)
	}
	return nil
}

func validateCodexState(state codexState, paths codexPaths, units []codexUnit) error {
	if state.Version != codexStateVersion {
		return errors.New("saved Codex state uses an unsupported version")
	}
	if state.ConfigPath != paths.configPath || state.ResolvedConfigPath != paths.resolvedConfigPath {
		return errors.New("saved Codex state belongs to a different configuration path")
	}
	for _, unit := range units {
		if _, ok := state.Entries[unit.id]; !ok {
			return errors.New("saved Codex state is incomplete")
		}
	}
	return nil
}

func codexConflicts(document codexDocument, state codexState, allowPrevious bool) ([]string, error) {
	conflicts := make([]string, 0)
	for _, unit := range codexUnits("") {
		current, err := document.value(unit)
		if err != nil {
			return nil, err
		}
		entry := state.Entries[unit.id]
		if current == entry.Applied || (allowPrevious && current == entry.Previous) {
			continue
		}
		conflicts = append(conflicts, unit.id)
	}
	sort.Strings(conflicts)
	return conflicts, nil
}

func codexMatchesPrevious(document codexDocument, state codexState) (bool, error) {
	for _, unit := range codexUnits("") {
		current, err := document.value(unit)
		if err != nil {
			return false, err
		}
		if current != state.Entries[unit.id].Previous {
			return false, nil
		}
	}
	return true, nil
}

func codexDriftError(conflicts []string) error {
	sort.Strings(conflicts)
	return fmt.Errorf("Codex configuration changed after Prism was enabled; refusing to overwrite: %s", strings.Join(conflicts, ", "))
}

func codexModeWithoutState(path string) (string, []string, error) {
	configData, _, _, err := readOptionalFile(path)
	if err != nil {
		return "", nil, fmt.Errorf("could not read Codex configuration: %w", err)
	}
	document := parseCodexDocument(configData)
	provider, err := document.value(codexUnit{id: "model_provider", kind: codexAssignment, name: "model_provider"})
	if err != nil {
		return "", nil, err
	}
	providerIsPrism := codexProviderIsPrism(provider)
	prismTable, err := document.value(codexUnit{id: "model_providers.prism", kind: codexTable, name: "model_providers.prism"})
	if err != nil {
		return "", nil, err
	}
	authTable, err := document.value(codexUnit{id: "model_providers.prism.auth", kind: codexTable, name: "model_providers.prism.auth"})
	if err != nil {
		return "", nil, err
	}
	if providerIsPrism && prismTable.Present && authTable.Present {
		return "prism", nil, nil
	}
	if !providerIsPrism {
		return "direct", nil, nil
	}
	conflicts := make([]string, 0, 3)
	if providerIsPrism || provider.Present {
		conflicts = append(conflicts, "model_provider")
	}
	if prismTable.Present {
		conflicts = append(conflicts, "model_providers.prism")
	}
	if authTable.Present {
		conflicts = append(conflicts, "model_providers.prism.auth")
	}
	return "drift", conflicts, nil
}

func codexProviderIsPrism(value codexManagedValue) bool {
	if !value.Present {
		return false
	}
	separator := strings.Index(value.Text, "=")
	if separator < 0 {
		return false
	}
	right := strings.TrimSpace(value.Text[separator+1:])
	for _, quoted := range []string{`"prism"`, `'prism'`} {
		if !strings.HasPrefix(right, quoted) {
			continue
		}
		remainder := strings.TrimSpace(strings.TrimPrefix(right, quoted))
		return remainder == "" || strings.HasPrefix(remainder, "#")
	}
	return false
}

func replaceFileIfUnchanged(
	path string,
	expected []byte,
	expectedExists bool,
	updated []byte,
	keepFile bool,
	mode fs.FileMode,
) error {
	current, currentExists, _, err := readOptionalFile(path)
	if err != nil {
		return err
	}
	if currentExists != expectedExists || !bytes.Equal(current, expected) {
		return errors.New("the file changed while Prism was updating it")
	}
	if !keepFile {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeFileAtomic(path, updated, mode)
}

func writeFileAtomic(path string, data []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".prism-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		if runtime.GOOS != "windows" {
			return err
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			return err
		}
		return os.Rename(temporaryPath, path)
	}
	return nil
}

func parseCodexDocument(data []byte) codexDocument {
	text := string(data)
	newline := "\n"
	if strings.Contains(text, "\r\n") {
		newline = "\r\n"
		text = strings.ReplaceAll(text, "\r\n", "\n")
	}
	return codexDocument{lines: strings.Split(text, "\n"), newline: newline}
}

func (document codexDocument) bytes() []byte {
	return []byte(strings.Join(document.lines, document.newline))
}

func (document codexDocument) value(unit codexUnit) (codexManagedValue, error) {
	switch unit.kind {
	case codexAssignment:
		index, found, err := document.assignmentIndex(unit.name)
		if err != nil {
			return codexManagedValue{}, err
		}
		if !found {
			return codexManagedValue{}, nil
		}
		return codexManagedValue{Present: true, Text: document.lines[index]}, nil
	case codexTable:
		start, end, found, err := document.tableRange(unit.name)
		if err != nil {
			return codexManagedValue{}, err
		}
		if !found {
			return codexManagedValue{}, nil
		}
		return codexManagedValue{Present: true, Text: strings.Join(document.lines[start:end], "\n")}, nil
	default:
		return codexManagedValue{}, errors.New("unsupported Codex configuration unit")
	}
}

func (document *codexDocument) set(unit codexUnit, value codexManagedValue) error {
	switch unit.kind {
	case codexAssignment:
		index, found, err := document.assignmentIndex(unit.name)
		if err != nil {
			return err
		}
		if found {
			if value.Present {
				document.lines[index] = value.Text
			} else {
				document.lines = append(document.lines[:index], document.lines[index+1:]...)
			}
			return nil
		}
		if value.Present {
			document.insertTopLevelLine(value.Text)
		}
		return nil
	case codexTable:
		start, end, found, err := document.tableRange(unit.name)
		if err != nil {
			return err
		}
		if found {
			replacement := []string(nil)
			if value.Present {
				replacement = strings.Split(value.Text, "\n")
			}
			document.lines = replaceLines(document.lines, start, end, replacement)
			return nil
		}
		if value.Present {
			document.appendTable(value.Text)
		}
		return nil
	default:
		return errors.New("unsupported Codex configuration unit")
	}
}

func (document codexDocument) assignmentIndex(key string) (int, bool, error) {
	found := -1
	for index, line := range document.lines {
		if _, ok := codexTableName(line); ok {
			break
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		separator := strings.Index(trimmed, "=")
		if separator < 0 || strings.TrimSpace(trimmed[:separator]) != key {
			continue
		}
		if found >= 0 {
			return 0, false, fmt.Errorf("Codex configuration contains duplicate %s assignments", key)
		}
		found = index
	}
	return found, found >= 0, nil
}

func (document codexDocument) tableRange(name string) (int, int, bool, error) {
	start := -1
	end := -1
	for index, line := range document.lines {
		tableName, ok := codexTableName(line)
		if !ok {
			continue
		}
		if start >= 0 && end < 0 {
			end = index
		}
		if tableName == name {
			if start >= 0 {
				return 0, 0, false, fmt.Errorf("Codex configuration contains duplicate [%s] tables", name)
			}
			start = index
		}
	}
	if start < 0 {
		return 0, 0, false, nil
	}
	if end < 0 {
		end = len(document.lines)
	}
	for end > start+1 && strings.TrimSpace(document.lines[end-1]) == "" {
		end--
	}
	return start, end, true, nil
}

func codexTableName(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") {
		return "", false
	}
	arrayTable := strings.HasPrefix(trimmed, "[[")
	closingToken := "]"
	openingLength := 1
	if arrayTable {
		closingToken = "]]"
		openingLength = 2
	}
	closing := strings.Index(trimmed[openingLength:], closingToken)
	if closing < 1 {
		return "", false
	}
	closing += openingLength
	remainder := strings.TrimSpace(trimmed[closing+len(closingToken):])
	if remainder != "" && !strings.HasPrefix(remainder, "#") {
		return "", false
	}
	name := strings.TrimSpace(trimmed[openingLength:closing])
	if arrayTable {
		name = "[]" + name
	}
	return name, true
}

func (document *codexDocument) insertTopLevelLine(line string) {
	index := len(document.lines)
	for current, candidate := range document.lines {
		if _, ok := codexTableName(candidate); ok {
			index = current
			break
		}
	}
	for index > 0 && strings.TrimSpace(document.lines[index-1]) == "" {
		index--
	}
	document.lines = replaceLines(document.lines, index, index, []string{line})
}

func (document *codexDocument) appendTable(text string) {
	block := strings.Split(text, "\n")
	if len(document.lines) == 1 && document.lines[0] == "" {
		document.lines = append(block, "")
		return
	}
	if document.lines[len(document.lines)-1] != "" {
		document.lines = append(document.lines, "")
	}
	document.lines = append(document.lines, block...)
	document.lines = append(document.lines, "")
}

func replaceLines(lines []string, start int, end int, replacement []string) []string {
	updated := make([]string, 0, len(lines)-(end-start)+len(replacement))
	updated = append(updated, lines[:start]...)
	updated = append(updated, replacement...)
	updated = append(updated, lines[end:]...)
	return updated
}

func printCodexHelp(output io.Writer) {
	_, _ = fmt.Fprintln(output, `Usage:
  prism codex enable
  prism codex disable
  prism codex status

Enable routes new Codex App and CLI tasks through Prism with gpt-5.6-sol.
Disable restores the settings that were present before enable.`)
}
