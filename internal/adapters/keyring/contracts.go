package keyring

import (
	"context"
	"errors"
	"sync"
)

var ErrNotFound = errors.New("secret not found")

type Store interface {
	Get(context.Context, string) ([]byte, error)
	Set(context.Context, string, []byte) error
	Delete(context.Context, string) error
}

type MemoryStore struct {
	mu     sync.RWMutex
	values map[string][]byte
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{values: make(map[string][]byte)} }

func (s *MemoryStore) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.values[key]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), value...), nil
}

func (s *MemoryStore) Set(ctx context.Context, key string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = append([]byte(nil), value...)
	return nil
}

func (s *MemoryStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.values[key]; !ok {
		return ErrNotFound
	}
	delete(s.values, key)
	return nil
}

var _ Store = (*MemoryStore)(nil)
