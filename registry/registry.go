package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

type Registry struct {
	Commands map[string]bool `json:"commands"`
}

func tuneDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tune")
}

func registryPath() string {
	return filepath.Join(tuneDir(), "commands.json")
}

func Load() (*Registry, error) {
	data, err := os.ReadFile(registryPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &Registry{Commands: map[string]bool{}}, nil
		}
		return nil, err
	}
	var r Registry
	if err := json.Unmarshal(data, &r); err != nil {
		return &Registry{Commands: map[string]bool{}}, nil
	}
	if r.Commands == nil {
		r.Commands = map[string]bool{}
	}
	return &r, nil
}

func (r *Registry) Save() error {
	if err := os.MkdirAll(tuneDir(), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(registryPath(), data, 0644)
}

func (r *Registry) Add(cmd string) {
	r.Commands[cmd] = true
}

func (r *Registry) Remove(cmd string) {
	delete(r.Commands, cmd)
}

func (r *Registry) Has(cmd string) bool {
	return r.Commands[cmd]
}

func (r *Registry) List() []string {
	cmds := make([]string, 0, len(r.Commands))
	for cmd := range r.Commands {
		cmds = append(cmds, cmd)
	}
	sort.Strings(cmds)
	return cmds
}
