package scm

// FoldersByName indexes folders by their Name field.
func FoldersByName(folders []Folder) map[string]Folder {
	byName := make(map[string]Folder, len(folders))
	for _, f := range folders {
		byName[f.Name] = f
	}
	return byName
}

// FolderAncestry returns name followed by every ancestor folder name up
// to the root, using each folder's Parent field. Stops at the first name
// not found in byName (e.g. the synthetic root) or on a cycle.
func FolderAncestry(name string, byName map[string]Folder) []string {
	chain := []string{name}
	seen := map[string]bool{name: true}
	for {
		f, ok := byName[chain[len(chain)-1]]
		if !ok || f.Parent == "" || seen[f.Parent] {
			return chain
		}
		chain = append(chain, f.Parent)
		seen[f.Parent] = true
	}
}

// DevicesUnderFolder returns every device whose folder ancestry includes
// folderName (directly or via an ancestor folder).
func DevicesUnderFolder(devices []Device, folders []Folder, folderName string) []Device {
	byName := FoldersByName(folders)
	var out []Device
	for _, d := range devices {
		for _, name := range FolderAncestry(d.Folder, byName) {
			if name == folderName {
				out = append(out, d)
				break
			}
		}
	}
	return out
}

// DevicesUnderSnippet returns every device whose folder ancestry includes
// a folder that snippetName is directly attached to.
func DevicesUnderSnippet(devices []Device, folders []Folder, snippetName string) []Device {
	byName := FoldersByName(folders)

	attached := map[string]bool{}
	for _, f := range folders {
		for _, s := range f.Snippets {
			if s == snippetName {
				attached[f.Name] = true
				break
			}
		}
	}

	var out []Device
	for _, d := range devices {
		for _, name := range FolderAncestry(d.Folder, byName) {
			if attached[name] {
				out = append(out, d)
				break
			}
		}
	}
	return out
}
