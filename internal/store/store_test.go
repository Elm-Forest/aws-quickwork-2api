package store

import (
	"context"
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestSaveUpsertsByEmailAndTenant(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	first := &Account{Email: "a@example.com", TenantURL: "https://tenant.example", Provider: "social", AccessToken: "one"}
	if err := st.Save(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := &Account{Email: "A@example.com", TenantURL: "https://tenant.example", Provider: "social", AccessToken: "two"}
	if err := st.Save(ctx, second); err != nil {
		t.Fatal(err)
	}
	if first.ID == 0 || second.ID != first.ID {
		t.Fatalf("expected same account id, got %d and %d", first.ID, second.ID)
	}
	list, err := st.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 account, got %d", len(list))
	}
	if list[0].AccessToken != "two" {
		t.Fatalf("expected updated token, got %q", list[0].AccessToken)
	}
}
