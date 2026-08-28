package reset

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Playbook is the parsed structure of a reset.yml file.
type Playbook struct {
	Name        string          `yaml:"name"`
	Push        bool            `yaml:"push"`
	FwList      []FirewallEntry `yaml:"fw_list"`
	FolderList  []FolderEntry   `yaml:"folder_list"`
	SnippetList []SnippetEntry  `yaml:"snippet_list"`
}

// FirewallEntry is one device entry under fw_list.
type FirewallEntry struct {
	Name   string `yaml:"name"`
	Serial string `yaml:"serial"`
}

// FolderEntry is one folder entry under folder_list. Name is what SCM's
// object-scoping APIs actually key on (folder=<name>), so it's required.
// ID is optional: if set, it's cross-checked against the folder's real,
// server-assigned id (from SCM's /folders resource) before anything is
// wiped, catching a stale/mistyped name pointing at the wrong folder.
type FolderEntry struct {
	Name string `yaml:"name"`
	ID   string `yaml:"id"`
}

// SnippetEntry is the snippet_list analogue of FolderEntry.
type SnippetEntry struct {
	Name string `yaml:"name"`
	ID   string `yaml:"id"`
}

// ResolvedFirewall is a validated FirewallEntry, ready for reset to act on.
type ResolvedFirewall struct {
	Name   string
	Serial string
}

// Resolve validates required fields.
func (fw FirewallEntry) Resolve() (ResolvedFirewall, error) {
	if fw.Serial == "" {
		return ResolvedFirewall{}, fmt.Errorf("fw_list entry %q: serial is required", fw.Name)
	}
	return ResolvedFirewall{Name: fw.Name, Serial: fw.Serial}, nil
}

// LoadPlaybook reads and parses a reset.yml file.
func LoadPlaybook(path string) (*Playbook, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading playbook: %w", err)
	}

	var pb Playbook
	if err := yaml.Unmarshal(data, &pb); err != nil {
		return nil, fmt.Errorf("parsing playbook: %w", err)
	}

	if len(pb.FwList) == 0 && len(pb.FolderList) == 0 && len(pb.SnippetList) == 0 {
		return nil, fmt.Errorf("playbook has no fw_list, folder_list, or snippet_list entries")
	}

	return &pb, nil
}

// Resolved returns every fw_list entry validated and ready to act on.
func (pb *Playbook) Resolved() ([]ResolvedFirewall, error) {
	out := make([]ResolvedFirewall, 0, len(pb.FwList))
	for _, fw := range pb.FwList {
		r, err := fw.Resolve()
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}
