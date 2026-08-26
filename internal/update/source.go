package update

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// syncPaths lists the repo-relative files/directories treated as
// gopangoblin's "own code" and refreshed by the update tool. Notably
// excluded: playbooks/ and secret.txt, which are the user's own runtime
// config/data, not the tool's source.
var syncPaths = []string{
	"main.go",
	"go.mod",
	"go.sum",
	"setup.ps",
	"README.md",
	".gitignore",
	"LICENSE-APACHE",
	"LICENSE-MIT",
	"internal",
}

// findRepoRoot walks upward from start looking for a directory containing
// go.mod, so "update" can be run from the gopangoblin repo root (as
// produced by setup.ps or a manual checkout).
func findRepoRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod found in %q or any parent directory; run this from the gopangoblin repo root", start)
		}
		dir = parent
	}
}

// downloadToTemp GETs url and saves the body to a temp file, returning its path.
func downloadToTemp(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}

	f, err := os.CreateTemp("", "gopangoblin-update-*.zip")
	if err != nil {
		return "", err
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// extractZip unpacks a GitHub source zip (which has a single top-level
// "<repo>-<branch>/" directory) into a fresh temp directory, stripping
// that top-level directory, and returns the temp directory's path along
// with a cleanup function.
func extractZip(zipPath string) (dir string, cleanup func(), err error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", nil, err
	}
	defer r.Close()

	tmpDir, err := os.MkdirTemp("", "gopangoblin-update-src-*")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { os.RemoveAll(tmpDir) }

	for _, f := range r.File {
		parts := strings.SplitN(f.Name, "/", 2)
		if len(parts) < 2 || parts[1] == "" {
			continue // the top-level directory entry itself
		}
		rel := parts[1]

		target := filepath.Join(tmpDir, rel)
		if !strings.HasPrefix(target, filepath.Clean(tmpDir)+string(os.PathSeparator)) {
			cleanup()
			return "", nil, fmt.Errorf("zip entry %q escapes extraction directory", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				cleanup()
				return "", nil, err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			cleanup()
			return "", nil, err
		}
		if err := extractZipFile(f, target); err != nil {
			cleanup()
			return "", nil, err
		}
	}

	return tmpDir, cleanup, nil
}

func extractZipFile(f *zip.File, target string) error {
	src, err := f.Open()
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
	if err != nil {
		return err
	}
	defer dst.Close()

	_, err = io.Copy(dst, src)
	return err
}

// syncSource copies the allowlisted source paths from srcDir into destRoot,
// overwriting whatever is already there.
func syncSource(srcDir, destRoot string) error {
	for _, rel := range syncPaths {
		src := filepath.Join(srcDir, rel)
		dst := filepath.Join(destRoot, rel)

		info, err := os.Stat(src)
		if os.IsNotExist(err) {
			continue // upstream may not have every optional file
		}
		if err != nil {
			return err
		}

		if info.IsDir() {
			if err := os.RemoveAll(dst); err != nil {
				return err
			}
			if err := copyDir(src, dst); err != nil {
				return err
			}
			continue
		}

		if err := copyFile(src, dst); err != nil {
			return err
		}
	}
	return nil
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// build runs "go build -o output ." in root.
func build(root, output string) error {
	cmd := exec.Command("go", "build", "-o", output, ".")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

func removeQuietly(path string) {
	_ = os.Remove(path)
}
