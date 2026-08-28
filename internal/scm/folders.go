package scm

import (
	"fmt"
	"net/url"
)

// Folder is a subset of the SCM "folders" resource: the container
// hierarchy that folder-scoped config objects (zones, rules, addresses,
// etc.) live in. Folders nest (each has a Parent), and a device's own
// per-device config is itself represented as a leaf folder in this same
// hierarchy -- but reset only ever targets folders explicitly listed in
// a playbook's folder_list, by name.
type Folder struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Parent string `json:"parent"`
}

type listFoldersResponse struct {
	Data   []Folder `json:"data"`
	Limit  int      `json:"limit"`
	Offset int      `json:"offset"`
	Total  int      `json:"total"`
}

// ListFolders returns every folder in this tenant, paginating as needed.
func (c *Client) ListFolders() ([]Folder, error) {
	const pageSize = 200

	var all []Folder
	offset := 0
	for {
		q := url.Values{}
		q.Set("limit", fmt.Sprintf("%d", pageSize))
		q.Set("offset", fmt.Sprintf("%d", offset))

		var page listFoldersResponse
		if err := c.doJSON("GET", "/config/setup/v1/folders", q, nil, &page); err != nil {
			return nil, fmt.Errorf("listing folders: %w", err)
		}
		all = append(all, page.Data...)

		offset += len(page.Data)
		if len(page.Data) == 0 || offset >= page.Total {
			break
		}
	}
	return all, nil
}

// ResolveFolderByName finds the folder with the given name.
func ResolveFolderByName(folders []Folder, name string) (Folder, error) {
	for _, f := range folders {
		if f.Name == name {
			return f, nil
		}
	}
	return Folder{}, fmt.Errorf("no folder found named %q", name)
}

// Snippet is a subset of the SCM "snippets" resource.
type Snippet struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type listSnippetsResponse struct {
	Data   []Snippet `json:"data"`
	Limit  int       `json:"limit"`
	Offset int       `json:"offset"`
	Total  int       `json:"total"`
}

// ListSnippets returns every snippet in this tenant, paginating as needed.
func (c *Client) ListSnippets() ([]Snippet, error) {
	const pageSize = 200

	var all []Snippet
	offset := 0
	for {
		q := url.Values{}
		q.Set("limit", fmt.Sprintf("%d", pageSize))
		q.Set("offset", fmt.Sprintf("%d", offset))

		var page listSnippetsResponse
		if err := c.doJSON("GET", "/config/setup/v1/snippets", q, nil, &page); err != nil {
			return nil, fmt.Errorf("listing snippets: %w", err)
		}
		all = append(all, page.Data...)

		offset += len(page.Data)
		if len(page.Data) == 0 || offset >= page.Total {
			break
		}
	}
	return all, nil
}

// ResolveSnippetByName finds the snippet with the given name.
func ResolveSnippetByName(snippets []Snippet, name string) (Snippet, error) {
	for _, s := range snippets {
		if s.Name == name {
			return s, nil
		}
	}
	return Snippet{}, fmt.Errorf("no snippet found named %q", name)
}
