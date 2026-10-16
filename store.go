package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
	"golang.org/x/crypto/bcrypt"
)

var linksBucket = []byte("links")

var (
	ErrLinkNotFound = errors.New("link not found")
	ErrImmutable    = errors.New("link was created without a password and can't be changed")
	ErrBadPassword  = errors.New("wrong password")
)

// maxPasswordLen is imposed by bcrypt, which refuses longer inputs.
const maxPasswordLen = 72

// Link is a stored filter addressable by a GUID. PasswordHash is empty for an
// immutable link.
type Link struct {
	ID           string    `json:"id"`
	Filter       Filter    `json:"filter"`
	PasswordHash string    `json:"password_hash,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Mutable reports whether the link can be updated with a password.
func (l *Link) Mutable() bool { return l.PasswordHash != "" }

type Store struct {
	db *bolt.DB
}

func OpenStore(path string) (*Store, error) {
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("can't open %s: %w", path, err)
	}

	err = db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(linksBucket)
		return err
	})
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("can't prepare %s: %w", path, err)
	}

	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Get(id string) (*Link, error) {
	var link *Link
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(linksBucket).Get([]byte(id))
		if raw == nil {
			return ErrLinkNotFound
		}
		link = &Link{}
		return json.Unmarshal(raw, link)
	})
	if err != nil {
		return nil, err
	}
	return link, nil
}

// Create stores a new filter under a fresh GUID. An empty password makes the
// link immutable.
func (s *Store) Create(filter Filter, password string) (*Link, error) {
	hash, err := hashPassword(password)
	if err != nil {
		return nil, err
	}

	id, err := newGUID()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	link := &Link{
		ID:           id,
		Filter:       filter,
		PasswordHash: hash,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := s.put(link); err != nil {
		return nil, err
	}
	return link, nil
}

// Update replaces the filter behind an existing GUID. The password itself can
// never be changed, only verified.
func (s *Store) Update(id string, filter Filter, password string) (*Link, error) {
	link, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	if !link.Mutable() {
		return nil, ErrImmutable
	}
	if err := bcrypt.CompareHashAndPassword([]byte(link.PasswordHash), []byte(password)); err != nil {
		return nil, ErrBadPassword
	}

	link.Filter = filter
	link.UpdatedAt = time.Now().UTC()

	if err := s.put(link); err != nil {
		return nil, err
	}
	return link, nil
}

func (s *Store) put(link *Link) error {
	raw, err := json.Marshal(link)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(linksBucket).Put([]byte(link.ID), raw)
	})
}

func hashPassword(password string) (string, error) {
	if password == "" {
		return "", nil
	}
	if len(password) > maxPasswordLen {
		return "", fmt.Errorf("password is longer than %d bytes", maxPasswordLen)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// newGUID returns a random UUIDv4.
func newGUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("can't generate id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
