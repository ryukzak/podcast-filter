package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func sampleFilter() Filter {
	return Filter{
		Feed:  "https://example.com/rss",
		Title: "Эхо (часть)",
		Rules: []Rule{{Re: "(?i)(Шульман)"}},
	}
}

func TestStoreCreateAndGet(t *testing.T) {
	store := testStore(t)

	link, err := store.Create(sampleFilter(), "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if !link.Mutable() {
		t.Fatal("a link created with a password should be mutable")
	}

	back, err := store.Get(link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.Filter.Title != "Эхо (часть)" || len(back.Filter.Rules) != 1 {
		t.Fatalf("filter lost: %+v", back.Filter)
	}
}

func TestStoreGetMissing(t *testing.T) {
	if _, err := testStore(t).Get("nope"); !errors.Is(err, ErrLinkNotFound) {
		t.Fatalf("err = %v, want ErrLinkNotFound", err)
	}
}

func TestStoreUpdate(t *testing.T) {
	store := testStore(t)
	link, err := store.Create(sampleFilter(), "hunter2")
	if err != nil {
		t.Fatal(err)
	}

	changed := sampleFilter()
	changed.Rules = append(changed.Rules, Rule{Re: "(?i)(LIVE)", Neg: true})

	updated, err := store.Update(link.ID, changed, "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Filter.Rules) != 2 {
		t.Fatalf("rules = %+v", updated.Filter.Rules)
	}
	if updated.ID != link.ID {
		t.Fatal("update must keep the same id")
	}
	if updated.PasswordHash != link.PasswordHash {
		t.Fatal("update must keep the same password")
	}
}

func TestStoreUpdateWrongPassword(t *testing.T) {
	store := testStore(t)
	link, _ := store.Create(sampleFilter(), "hunter2")

	if _, err := store.Update(link.ID, sampleFilter(), "wrong"); !errors.Is(err, ErrBadPassword) {
		t.Fatalf("err = %v, want ErrBadPassword", err)
	}
}

func TestStoreUpdateImmutable(t *testing.T) {
	store := testStore(t)
	link, err := store.Create(sampleFilter(), "")
	if err != nil {
		t.Fatal(err)
	}
	if link.Mutable() {
		t.Fatal("a link created without a password should be immutable")
	}

	// An empty password must not be a way around immutability.
	if _, err := store.Update(link.ID, sampleFilter(), ""); !errors.Is(err, ErrImmutable) {
		t.Fatalf("err = %v, want ErrImmutable", err)
	}
}

func TestStoreUpdateMissing(t *testing.T) {
	if _, err := testStore(t).Update("nope", sampleFilter(), "x"); !errors.Is(err, ErrLinkNotFound) {
		t.Fatalf("err = %v, want ErrLinkNotFound", err)
	}
}

func TestStoreRejectsOverlongPassword(t *testing.T) {
	store := testStore(t)
	if _, err := store.Create(sampleFilter(), strings.Repeat("x", maxPasswordLen+1)); err == nil {
		t.Fatal("expected an error for an overlong password")
	}
}

func TestStoreSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	link, err := store.Create(sampleFilter(), "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()

	if _, err := reopened.Get(link.ID); err != nil {
		t.Fatalf("link lost after reopen: %v", err)
	}
}

func TestNewGUIDIsUniqueAndWellFormed(t *testing.T) {
	seen := make(map[string]bool)
	for range 100 {
		id, err := newGUID()
		if err != nil {
			t.Fatal(err)
		}
		if len(id) != 36 || id[14] != '4' {
			t.Fatalf("malformed uuid: %s", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id: %s", id)
		}
		seen[id] = true
	}
}
