package fsaccess

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/spadeide/spade-agent/internal/protocol"
)

func (s *Service) List(path string) (*protocol.ListResult, error) {
	abs, err := s.resolve(path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	out := make([]protocol.DirEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, protocol.DirEntry{
			Name:    e.Name(),
			Path:    filepath.Join(abs, e.Name()),
			IsDir:   e.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().UnixMilli(),
		})
	}
	return &protocol.ListResult{Entries: out}, nil
}

func (s *Service) Stat(path string) (*protocol.DirEntry, error) {
	abs, err := s.resolve(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	return &protocol.DirEntry{
		Name:    info.Name(),
		Path:    abs,
		IsDir:   info.IsDir(),
		Size:    info.Size(),
		ModTime: info.ModTime().UnixMilli(),
	}, nil
}

func (s *Service) Read(path string) (*protocol.ReadResult, error) {
	abs, err := s.resolve(path)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(abs); err == nil {
		if info.IsDir() {
			return nil, fmt.Errorf("is a directory")
		}
		if info.Size() > MaxFileBytes {
			return nil, fmt.Errorf("file too large: %d bytes (max %d)", info.Size(), MaxFileBytes)
		}
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	enc := "binary"
	if utf8.Valid(data) {
		enc = "utf-8"
	}
	return &protocol.ReadResult{
		Path:     abs,
		Content:  base64.StdEncoding.EncodeToString(data),
		Encoding: enc,
	}, nil
}

func (s *Service) Write(path, contentB64 string) (*protocol.WriteResult, error) {
	abs, err := s.resolveWritable(path)
	if err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(contentB64)
	if err != nil {
		return nil, fmt.Errorf("invalid base64")
	}
	if len(data) > MaxFileBytes {
		return nil, fmt.Errorf("file too large: %d bytes (max %d)", len(data), MaxFileBytes)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(abs, data, 0o644); err != nil {
		return nil, err
	}
	s.chownUp(abs)
	return &protocol.WriteResult{Path: abs, Bytes: len(data)}, nil
}

func (s *Service) Mkdir(path string) error {
	abs, err := s.resolveWritable(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return err
	}
	s.chownUp(abs)
	return nil
}

func (s *Service) Delete(path string) error {
	abs, err := s.resolveWritable(path)
	if err != nil {
		return err
	}
	return os.RemoveAll(abs)
}

func (s *Service) Rename(from, to string) (string, error) {
	absFrom, err := s.resolveWritable(from)
	if err != nil {
		return "", err
	}
	absTo, err := s.resolveWritable(to)
	if err != nil {
		return "", err
	}
	if err := os.Rename(absFrom, absTo); err != nil {
		return "", err
	}
	s.chownUp(absTo)
	return absTo, nil
}
