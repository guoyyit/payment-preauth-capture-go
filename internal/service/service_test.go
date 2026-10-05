package service

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/guoyyit/payment-preauth-capture-go/internal/domain"
)

// fakeClock 可手动推进的时钟。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestService(t *testing.T) (*Service, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	svc, err := New(Config{Now: clock.now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc, clock
}

func newPersistentService(t *testing.T, clock *fakeClock) *Service {
	t.Helper()
	path := filepath.Join(t.TempDir(), "snapshot.json")
	svc, err := New(Config{SnapshotPath: path, Now: clock.now})
	if err != nil {
		t.Fatalf("New persistent: %v", err)
	}
	return svc
}

func reopenPersistentService(t *testing.T, path string, clock *fakeClock) *Service {
	t.Helper()
	svc, err := New(Config{SnapshotPath: path, Now: clock.now})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	return svc
}

func snapshotPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "snapshot.json")
}

func mustCreateAuth(t *testing.T, svc *Service, accountID string, amount domain.Money, ttl time.Duration) *domain.PreAuth {
	t.Helper()
	auth, err := svc.CreatePreAuth(CreateAuthRequest{
		IdempotencyKey: "auth-" + accountID + "-" + randomID("k"),
		AccountID:      accountID,
		Amount:         amount,
		TTLMillis:      ttl.Milliseconds(),
	})
	if err != nil {
		t.Fatalf("CreatePreAuth: %v", err)
	}
	return auth
}

func mustAccount(t *testing.T, svc *Service, id string) *domain.Account {
	t.Helper()
	acc, err := svc.GetAccount(id)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	return acc
}
