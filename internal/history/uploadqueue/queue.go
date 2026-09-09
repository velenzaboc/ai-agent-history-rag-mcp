// Package uploadqueue durably stages parsed work for a later uploader.
package uploadqueue

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

var (
	ErrFull    = errors.New("upload queue full")
	ErrCorrupt = errors.New("corrupt upload queue")
	ErrInvalid = errors.New("invalid upload record")
)

type Item struct {
	ID, Source, Payload string
	Attempts            int
	Next                time.Time
}
type Queue struct {
	mu       sync.Mutex
	path     string
	capacity int
	items    map[string]Item
}
type disk struct {
	Version int    `json:"version"`
	Items   []Item `json:"items"`
}

func Open(path string, capacity int) (*Queue, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || capacity < 1 {
		return nil, ErrInvalid
	}
	q := &Queue{path: path, capacity: capacity, items: map[string]Item{}}
	b, e := os.ReadFile(path)
	if errors.Is(e, os.ErrNotExist) {
		return q, nil
	}
	if e != nil {
		return nil, e
	}
	var d disk
	if json.Unmarshal(b, &d) != nil || d.Version != 1 {
		return nil, ErrCorrupt
	}
	if len(d.Items) > capacity {
		return nil, ErrCorrupt
	}
	for _, v := range d.Items {
		if valid(v) != nil {
			return nil, ErrCorrupt
		}
		if _, ok := q.items[v.ID]; ok {
			return nil, ErrCorrupt
		}
		q.items[v.ID] = v
	}
	return q, nil
}
func (q *Queue) Enqueue(v Item) error {
	if e := valid(v); e != nil {
		return e
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if old, ok := q.items[v.ID]; ok {
		if old == v {
			return nil
		}
		return ErrInvalid
	}
	if len(q.items) >= q.capacity {
		return ErrFull
	}
	next := copyItems(q.items)
	next[v.ID] = v
	if e := save(q.path, next); e != nil {
		return e
	}
	q.items = next
	return nil
}
func (q *Queue) Ready(now time.Time, limit int) []Item {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := []Item{}
	for _, v := range q.items {
		if !v.Next.After(now) {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
func (q *Queue) Ack(id string) error {
	return q.change(id, func(Item) (Item, bool, error) { return Item{}, false, nil })
}
func (q *Queue) Retry(id string, next time.Time) error {
	if next.IsZero() {
		return ErrInvalid
	}
	return q.change(id, func(v Item) (Item, bool, error) { v.Attempts++; v.Next = next.UTC(); return v, true, nil })
}
func (q *Queue) change(id string, f func(Item) (Item, bool, error)) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	v, ok := q.items[id]
	if !ok {
		return nil
	}
	replacement, keep, e := f(v)
	if e != nil {
		return e
	}
	next := copyItems(q.items)
	if keep {
		next[id] = replacement
	} else {
		delete(next, id)
	}
	if e = save(q.path, next); e != nil {
		return e
	}
	q.items = next
	return nil
}
func valid(v Item) error {
	if v.ID == "" || v.Source == "" || v.Payload == "" || v.Attempts < 0 || v.Next.IsZero() {
		return ErrInvalid
	}
	return nil
}
func copyItems(in map[string]Item) map[string]Item {
	out := make(map[string]Item, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
func save(path string, items map[string]Item) error {
	list := make([]Item, 0, len(items))
	for _, v := range items {
		v.Next = v.Next.UTC()
		list = append(list, v)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	b, e := json.Marshal(disk{1, list})
	if e != nil {
		return e
	}
	dir := filepath.Dir(path)
	if e = os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, ".queue-")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	if c := f.Close(); e == nil {
		e = c
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
