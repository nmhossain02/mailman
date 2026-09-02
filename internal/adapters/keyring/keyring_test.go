package keyring

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type failingKeyring struct{ err error }

func (f failingKeyring) Get(string, string) (string, error) { return "", f.err }
func (f failingKeyring) Set(string, string, string) error   { return f.err }
func (f failingKeyring) Delete(string, string) error        { return f.err }

func TestKeyringErrorPropagation(t *testing.T) {
	want := errors.New("desktop keyring locked")
	s, err := NewKeyringStoreWithBackend("mailman", failingKeyring{want})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(context.Background(), "token"); err == nil || !errors.Is(err, want) || !strings.Contains(err.Error(), "OS keyring unavailable") {
		t.Fatalf("Get error = %v", err)
	}
	if err = s.Set(context.Background(), "token", []byte("secret")); !errors.Is(err, want) {
		t.Fatalf("Set error = %v", err)
	}
	if err = s.Delete(context.Background(), "token"); !errors.Is(err, want) {
		t.Fatalf("Delete error = %v", err)
	}
}
