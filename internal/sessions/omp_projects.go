package sessions

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// OmpProject is a folder omp was run in, as its sessions name it: the cwd
// of their header, as omp spells it (a distro's /home/me/x for an omp in
// WSL), and when a session there last changed.
type OmpProject struct {
	Dir  string    `json:"dir"`
	Last time.Time `json:"last"`
}

// OmpProjects are the folders omp's sessions under roots (an agent
// folder's sessions/, omp keeping one folder per working directory there)
// were run in, the newest first. Each folder's newest session is read up
// to its header for the cwd, which the folder's name spells lossily (Pi's
// --C--Users-me-x--). None while magpie doesn't read omp's sessions
// (Reads): the advisor's project list (#5019) offers what it read only.
func OmpProjects(roots ...string) []OmpProject {
	if !Reads("omp") {
		return nil
	}
	seen := map[string]int{}
	var out []OmpProject
	for _, root := range roots {
		dirs, _ := os.ReadDir(root)
		for _, d := range dirs {
			if !d.IsDir() {
				continue
			}
			newest, at := "", time.Time{}
			files, _ := filepath.Glob(filepath.Join(root, d.Name(), "*.jsonl"))
			for _, f := range files {
				if fi, err := os.Stat(f); err == nil && fi.Mode().IsRegular() && fi.ModTime().After(at) {
					newest, at = f, fi.ModTime()
				}
			}
			cwd := ompHeaderCwd(newest)
			if cwd == "" {
				continue
			}
			if i, ok := seen[cwd]; ok {
				if at.After(out[i].Last) {
					out[i].Last = at
				}
				continue
			}
			seen[cwd] = len(out)
			out = append(out, OmpProject{Dir: cwd, Last: at})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Last.After(out[j].Last) })
	return out
}

// ompHeaderCwd is the cwd of the session header in an omp session file:
// its "session" line, after the fixed-width title line omp writes first.
// Only the first few lines are read; "" when there is no header there.
func ompHeaderCwd(path string) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for i := 0; i < 4 && sc.Scan(); i++ {
		b := sc.Bytes()
		if !bytes.Contains(b, piHeader) {
			continue
		}
		var l piLine
		if json.Unmarshal(b, &l) == nil && l.Type == "session" {
			return strings.TrimSpace(l.Cwd)
		}
	}
	return ""
}
