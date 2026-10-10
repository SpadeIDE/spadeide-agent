package fsaccess

import (
	"fmt"

	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"
	"github.com/spadeide/spade-agent/internal/protocol"
)

func (s *Service) Watch(path string) (string, error) {
	abs, err := s.resolve(path)
	if err != nil {
		return "", err
	}
	real := realPath(abs)

	s.mu.Lock()
	if existing := s.byPath[real]; existing != nil {
		existing.refs++
		s.mu.Unlock()
		return existing.id, nil
	}
	if len(s.byPath) >= MaxWatches {
		s.mu.Unlock()
		return "", fmt.Errorf("too many watches")
	}
	s.mu.Unlock()

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return "", err
	}
	if err := w.Add(abs); err != nil {
		_ = w.Close()
		return "", err
	}
	id := uuid.NewString()
	entry := &watchEntry{id: id, path: real, w: w, refs: 1}

	s.mu.Lock()
	if existing := s.byPath[real]; existing != nil {
		// Lost the race — reuse the winner and drop ours.
		existing.refs++
		s.mu.Unlock()
		_ = w.Close()
		return existing.id, nil
	}
	if len(s.byPath) >= MaxWatches {
		s.mu.Unlock()
		_ = w.Close()
		return "", fmt.Errorf("too many watches")
	}
	s.watch[id] = entry
	s.byPath[real] = entry
	s.mu.Unlock()

	go func() {
		for {
			select {
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				op := "write"
				switch {
				case ev.Op&fsnotify.Create != 0:
					op = "create"
				case ev.Op&fsnotify.Remove != 0:
					op = "remove"
				case ev.Op&fsnotify.Rename != 0:
					op = "rename"
				case ev.Op&fsnotify.Write != 0:
					op = "write"
				}
				if s.onEvt != nil {
					s.onEvt(protocol.NewEvent(protocol.EventFSChanged, protocol.FSChangedParams{
						WatchID: id,
						Path:    ev.Name,
						Op:      op,
					}))
				}
			case _, ok := <-w.Errors:
				if !ok {
					return
				}
			}
		}
	}()
	return id, nil
}

func (s *Service) Unwatch(id string) error {
	s.mu.Lock()
	entry, ok := s.watch[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("watch not found")
	}
	entry.refs--
	if entry.refs > 0 {
		s.mu.Unlock()
		return nil
	}
	delete(s.watch, id)
	delete(s.byPath, entry.path)
	s.mu.Unlock()
	return entry.w.Close()
}

// ClearWatches drops every directory watch. Called on workspace.open so a
// client that never sends fs.unwatch cannot accumulate watchers across projects.
func (s *Service) ClearWatches() {
	s.mu.Lock()
	entries := make([]*watchEntry, 0, len(s.watch))
	for id, entry := range s.watch {
		entries = append(entries, entry)
		delete(s.watch, id)
	}
	s.byPath = make(map[string]*watchEntry)
	s.mu.Unlock()
	for _, entry := range entries {
		_ = entry.w.Close()
	}
}

func (s *Service) Close() {
	s.mu.Lock()
	if s.grepCancel != nil {
		s.grepCancel()
		s.grepCancel = nil
	}
	s.mu.Unlock()
	s.ClearWatches()
}
