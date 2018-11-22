// Package manifest contains the type and methods used for synthesizing
// and manipulating manifest files outlining the contents of a particular
// directory when it is joined together by an overlayfs.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	// "reflect"
	"sort"
)

// Manifest is used to {en,de}code a bill of materials of sorts that outlines
// the contents of all of the files contained in a particular directory.
type Manifest struct {
	// relative path -> SHA256 (or empty if a directory)
	Files map[string]string `json:"files,omitempty"`
}

func (m *Manifest) Has(path string) bool {
	_, ok := m.Files[normalize(path)]
	return ok
}

func (m *Manifest) Add(path string, hash string) {
	if m.Files == nil {
		m.Files = make(map[string]string)
	}
	m.Files[normalize(path)] = hash
}

func (m *Manifest) Remove(path string) {
	delete(m.Files, normalize(path))
}

func (m *Manifest) Missing(paths []string) []string {
	have := make(map[string]struct{})
	for _, path := range paths {
		have[normalize(path)] = struct{}{}
	}

	var missing sort.StringSlice
	for key := range m.Files {
		if _, ok := have[key]; ok {
			continue
		}
		missing = append(missing, key)
	}
	missing.Sort()
	return []string(missing)
}

// TODO(#11): Can't have Fixed with a TODO of the same issue remaining.
func Value(path string, st os.FileInfo) (string, error) {
	if st.IsDir() {
		return "", nil
	}

	return digest(path)
}

func normalize(path string) string {
	return filepath.Clean(path)
}

func digest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
