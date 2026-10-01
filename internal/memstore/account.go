package memstore

import (
	"context"
	"fmt"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// AddAccount stores info as an account with a fresh version, for test setup.
func (s *Store) AddAccount(info meta.AccountInfo) *op.AccountRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := &op.AccountRecord{
		Info:    info,
		Attrs:   map[string][]byte{},
		Version: meta.ObjVersion{Ver: 1, Tag: s.newTag()},
		Mtime:   s.now(),
	}
	s.accounts[info.ID] = rec
	return copyAccount(rec)
}

// GetAccount implements op.AccountStore.
func (s *Store) GetAccount(_ context.Context, id string) (*op.AccountRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.accounts[id]
	if !ok {
		return nil, fmt.Errorf("account %s: %w", id, op.ErrNoSuchEntity)
	}
	return copyAccount(rec), nil
}

// AccountName implements op.AccountStore.
func (s *Store) AccountName(ctx context.Context, id string) (string, error) {
	rec, err := s.GetAccount(ctx, id)
	if err != nil {
		return "", err
	}
	return rec.Info.Name, nil
}

func copyAccount(rec *op.AccountRecord) *op.AccountRecord {
	c := *rec
	c.Attrs = cloneAttrs(rec.Attrs)
	return &c
}
