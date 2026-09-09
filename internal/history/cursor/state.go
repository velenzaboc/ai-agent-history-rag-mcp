// Package cursor persists validated per-source checkpoints locally.
package cursor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const version = 1

var (
	ErrInvalid = errors.New("invalid cursor state")
	ErrCorrupt = errors.New("corrupt cursor state")
)

type Checkpoint struct {
	Source, Path, Digest string
	Generation, Offset   int64
	Updated              time.Time
}
type State struct {
	mu      sync.Mutex
	path    string
	entries map[string]Checkpoint
}
type disk struct {
	Version int          `json:"version"`
	Entries []Checkpoint `json:"entries"`
}

func Open(path string) (*State, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrInvalid
	}
	s := &State{path: path, entries: map[string]Checkpoint{}}
	b, e := os.ReadFile(path)
	if errors.Is(e, os.ErrNotExist) {
		return s, nil
	}
	if e != nil {
		return nil, e
	}
	var d disk
	if json.Unmarshal(b, &d) != nil {
		return nil, ErrCorrupt
	}
	if d.Version != version {
		return nil, ErrCorrupt
	}
	for _, c := range d.Entries {
		if validate(c) != nil {
			return nil, ErrCorrupt
		}
		if _, ok := s.entries[c.Source]; ok {
			return nil, ErrCorrupt
		}
		s.entries[c.Source] = c
	}
	return s, nil
}
func (s *State) Get(source string) (Checkpoint, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.entries[source]
	return v, ok
}
func (s *State) Put(c Checkpoint) error {
	if e := validate(c); e != nil {
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.entries[c.Source]; ok && c.Generation < old.Generation {
		return ErrInvalid
	}
	next := make(map[string]Checkpoint, len(s.entries)+1)
	for k, v := range s.entries {
		next[k] = v
	}
	next[c.Source] = c
	if e := persist(s.path, next); e != nil {
		return e
	}
	s.entries = next
	return nil
}
func validate(c Checkpoint) error {
	if c.Source == "" || strings.ContainsAny(c.Source, "/\\") || !filepath.IsAbs(c.Path) || filepath.Clean(c.Path) != c.Path || c.Generation < 0 || c.Offset < 0 || len(c.Digest) != 64 || c.Updated.IsZero() {
		return ErrInvalid
	}
	if _, e := hex.DecodeString(c.Digest); e != nil {
		return ErrInvalid
	}
	return nil
}
func persist(path string, entries map[string]Checkpoint) error {
	list := make([]Checkpoint, 0, len(entries))
	for _, v := range entries {
		v.Updated = v.Updated.UTC()
		list = append(list, v)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Source < list[j].Source })
	b, e := json.Marshal(disk{version, list})
	if e != nil {
		return e
	}
	dir := filepath.Dir(path)
	if e = os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	tmp, e := os.CreateTemp(dir, ".cursor-")
	if e != nil {
		return e
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, e = tmp.Write(b); e == nil {
		e = tmp.Sync()
	}
	if close := tmp.Close(); e == nil {
		e = close
	}
	if e != nil {
		return e
	}
	if e = os.Rename(name, path); e != nil {
		return e
	}
	d, e := os.Open(dir)
	if e == nil {
		e = d.Sync()
		d.Close()
	}
	return e
}
func Digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func (s *State) Path() string   { return s.path }
func (s *State) String() string { return fmt.Sprintf("cursor state %s", s.path) }
