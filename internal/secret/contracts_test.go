package secret

import (
	"context"
	"errors"
	"testing"
)

func TestMemoryStoreCopiesValues(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	input := []byte("token")
	if err := store.Set(context.Background(), "key", input); err != nil {
		t.Fatal(err)
	}
	input[0] = 'X'
	got, err := store.Get(context.Background(), "key")
	if err != nil {
		t.Fatal(err)
	}
	got[0] = 'Y'
	again, err := store.Get(context.Background(), "key")
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != "token" {
		t.Fatalf("stored secret mutated: %q", again)
	}
}

func TestMemoryStoreDeleteMissing(t *testing.T) {
	t.Parallel()
	if err := NewMemoryStore().Delete(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete() error = %v, want ErrNotFound", err)
	}
}
