package secret

import (
	"context"
	"errors"
	"fmt"

	keyring "github.com/zalando/go-keyring"
)

// KeyringBackend is the narrow seam used to test platform keyring failures.
type KeyringBackend interface {
	Get(service, user string) (string, error)
	Set(service, user, password string) error
	Delete(service, user string) error
}

type platformKeyring struct{}

func (platformKeyring) Get(service, user string) (string, error) { return keyring.Get(service, user) }
func (platformKeyring) Set(service, user, password string) error {
	return keyring.Set(service, user, password)
}
func (platformKeyring) Delete(service, user string) error { return keyring.Delete(service, user) }

type KeyringStore struct {
	service string
	backend KeyringBackend
}

func NewKeyringStore(service string) (*KeyringStore, error) {
	if service == "" {
		return nil, errors.New("keyring service must not be empty")
	}
	return &KeyringStore{service: service, backend: platformKeyring{}}, nil
}

func NewKeyringStoreWithBackend(service string, backend KeyringBackend) (*KeyringStore, error) {
	if service == "" || backend == nil {
		return nil, errors.New("keyring service and backend are required")
	}
	return &KeyringStore{service: service, backend: backend}, nil
}

func (s *KeyringStore) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	v, err := s.backend.Get(s.service, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("OS keyring unavailable: %w", err)
	}
	return []byte(v), nil
}

func (s *KeyringStore) Set(ctx context.Context, key string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.backend.Set(s.service, key, string(value)); err != nil {
		return fmt.Errorf("OS keyring unavailable: %w", err)
	}
	return nil
}

func (s *KeyringStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := s.backend.Delete(s.service, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("OS keyring unavailable: %w", err)
	}
	return nil
}

var _ Store = (*KeyringStore)(nil)
