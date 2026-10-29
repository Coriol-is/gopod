package backup

import (
	"os"
	"path/filepath"
	"sort"
)

// Summary is one published set as listed; it is read from the manifest only
// (no hashing), so listing is cheap and does not prove the set is intact.
type Summary struct {
	SetID       string `json:"set_id"`
	Mode        string `json:"mode,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
	DBBytes     int64  `json:"db_bytes,omitempty"`
	Files       int    `json:"files,omitempty"`
	Sealed      bool   `json:"sealed"`
	Problem     string `json:"problem,omitempty"`
}

// List returns the published sets under root, oldest first. The staging
// directory and anything that is not a set-shaped directory is ignored.
func List(root string) ([]Summary, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []Summary
	for _, e := range entries {
		if !e.IsDir() || !setIDPattern.MatchString(e.Name()) {
			continue
		}
		s := Summary{SetID: e.Name()}
		dir := filepath.Join(root, e.Name())
		raw, merr := readSmall(filepath.Join(dir, ManifestName))
		marker, kerr := readSmall(filepath.Join(dir, MarkerName))
		switch {
		case merr != nil || kerr != nil:
			s.Problem = "manifest or completion marker missing"
		case string(marker) != string(markerFor(raw)):
			s.Problem = "completion marker does not match the manifest"
		default:
			s.Sealed = true
			if m, perr := parseManifest(raw); perr != nil {
				s.Sealed, s.Problem = false, perr.Error()
			} else {
				s.Mode, s.CompletedAt, s.DBBytes, s.Files = m.Mode, m.CompletedAt, m.Database.Size, len(m.Files)+1
			}
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SetID < out[j].SetID })
	return out, nil
}
