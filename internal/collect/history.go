package collect

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/YoanWai/runrack/internal/state"
)

const historySchema = "runrack.history/v1"

type historyFile struct {
	Schema string         `json:"schema"`
	Recent []state.Result `json:"recent"`
}

// HistoryPath is where the board keeps finished jobs between runs, following the XDG state directory.
func HistoryPath(getenv func(string) string) string {
	if dir := getenv("XDG_STATE_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "runrack", "history.json")
	}
	if home := getenv("HOME"); filepath.IsAbs(home) {
		return filepath.Join(home, ".local", "state", "runrack", "history.json")
	}
	return ""
}

func loadHistory(path string) ([]state.Result, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var file historyFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if file.Schema != historySchema {
		return nil, fmt.Errorf("%s: schema %q, want %q", path, file.Schema, historySchema)
	}
	return file.Recent, nil
}

// saveHistory replaces the file in one rename, so a crash or a second runrack never leaves half a file.
func saveHistory(path string, recent []state.Result) error {
	data, err := json.Marshal(historyFile{Schema: historySchema, Recent: recent})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".history-*.json")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	err = errors.Join(err, tmp.Close())
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		return errors.Join(err, os.Remove(tmp.Name()))
	}
	return nil
}
