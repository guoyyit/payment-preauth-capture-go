package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/guoyyit/payment-preauth-capture-go/domain"
)

// snapshot is the single unit of durability. Committing a transaction replaces
// the whole snapshot on disk atomically, which gives all-or-nothing recovery.
type snapshot struct {
	Accounts    map[string]*domain.Account          `json:"accounts"`
	PreAuths    map[string]*domain.PreAuth          `json:"preauths"`
	Captures    map[string]*domain.Capture          `json:"captures"`
	Voids       map[string]*domain.VoidRecord       `json:"voids"`
	Refunds     map[string]*domain.Refund           `json:"refunds"`
	Events      []*domain.Event                     `json:"events"`
	Idempotency map[string]*domain.IdempotentRecord `json:"idempotency"`
	Batches     map[string]*domain.SettlementBatch  `json:"batches"`
}

func newSnapshot() *snapshot {
	return &snapshot{
		Accounts:    map[string]*domain.Account{},
		PreAuths:    map[string]*domain.PreAuth{},
		Captures:    map[string]*domain.Capture{},
		Voids:       map[string]*domain.VoidRecord{},
		Refunds:     map[string]*domain.Refund{},
		Events:      []*domain.Event{},
		Idempotency: map[string]*domain.IdempotentRecord{},
		Batches:     map[string]*domain.SettlementBatch{},
	}
}

func (s *snapshot) clone() *snapshot {
	cp := newSnapshot()
	for k, v := range s.Accounts {
		ac := *v
		cp.Accounts[k] = &ac
	}
	for k, v := range s.PreAuths {
		p := *v
		cp.PreAuths[k] = &p
	}
	for k, v := range s.Captures {
		c := *v
		if v.RefundIDs != nil {
			c.RefundIDs = append([]string(nil), v.RefundIDs...)
		}
		cp.Captures[k] = &c
	}
	for k, v := range s.Refunds {
		r := *v
		cp.Refunds[k] = &r
	}
	for k, v := range s.Voids {
		r := *v
		cp.Voids[k] = &r
	}
	cp.Events = append(cp.Events, s.Events...)
	for k, v := range s.Idempotency {
		r := *v
		cp.Idempotency[k] = &r
	}
	for k, v := range s.Batches {
		b := *v
		b.CaptureIDs = append([]string(nil), v.CaptureIDs...)
		cp.Batches[k] = &b
	}
	return cp
}

// FileStore implementation ---------------------------------------------------

type fileStore struct {
	mu   sync.RWMutex
	path string
	data *snapshot
}

func NewFileStore(path string) (*FileStore, error) {
	fs := &fileStore{path: path}
	if err := fs.load(); err != nil {
		return nil, err
	}
	return fs, nil
}

// Path returns the on-disk snapshot file location.
func (s *fileStore) Path() string {
	return s.path
}

func (st *fileStore) load() error {
	raw, err := os.ReadFile(st.path)
	if err != nil {
		if os.IsNotExist(err) {
			st.data = newSnapshot()
			return nil
		}
		return err
	}
	snap := newSnapshot()
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, snap); err != nil {
			return fmt.Errorf("corrupt store file %q: %w", st.path, err)
		}
	}
	st.data = snap
	return nil
}

func (st *fileStore) persist(snap *snapshot) error {
	if err := os.MkdirAll(filepath.Dir(st.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(st.path)
	tmp, err := os.CreateTemp(dir, ".store-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, st.path); err != nil {
		os.Remove(tmpName)
		return err
	}
	if dirf, err := os.Open(dir); err == nil {
		dirf.Sync()
		dirf.Close()
	}
	return nil
}

func (s *fileStore) Update(ctx context.Context, fn func(repo Repository) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	work := s.data.clone()
	tx := &txRepo{snap: work}
	if err := fn(tx); err != nil {
		return err
	}
	if err := s.persist(work); err != nil {
		return err
	}
	s.data = work
	return nil
}

func (s *fileStore) Read(ctx context.Context, fn func(repo Repository) error) error {
	s.mu.RLock()
	view := s.data.clone()
	s.mu.RUnlock()
	return fn(&txRepo{snap: view})
}

// txRepo is the in-transaction Repository implementation.
type txRepo struct {
	snap *snapshot
}

func (r *txRepo) CreateAccount(ctx context.Context, account *domain.Account) error {
	if _, ok := r.snap.Accounts[account.ID]; ok {
		return fmt.Errorf("%w: %s", domain.ErrDuplicateRequest, account.ID)
	}
	cp := *account
	r.snap.Accounts[account.ID] = &cp
	return nil
}

func (r *txRepo) GetAccount(ctx context.Context, id string) (*domain.Account, error) {
	a, ok := r.snap.Accounts[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", domain.ErrAccountNotFound, id)
	}
	return a, nil
}

func (r *txRepo) PutPreAuth(ctx context.Context, preauth *domain.PreAuth) error {
	cp := *preauth
	r.snap.PreAuths[preauth.ID] = &cp
	return nil
}

func (r *txRepo) GetPreAuth(ctx context.Context, id string) (*domain.PreAuth, error) {
	p, ok := r.snap.PreAuths[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", domain.ErrPreAuthNotFound, id)
	}
	return p, nil
}

func (r *txRepo) ListPreAuths(ctx context.Context) ([]*domain.PreAuth, error) {
	out := make([]*domain.PreAuth, 0, len(r.snap.PreAuths))
	for _, p := range r.snap.PreAuths {
		out = append(out, p)
	}
	return out, nil
}

func (r *txRepo) PutCapture(ctx context.Context, capture *domain.Capture) error {
	cp := *capture
	if capture.RefundIDs != nil {
		cp.RefundIDs = append([]string(nil), capture.RefundIDs...)
	}
	r.snap.Captures[capture.ID] = &cp
	return nil
}

func (r *txRepo) GetCapture(ctx context.Context, id string) (*domain.Capture, error) {
	c, ok := r.snap.Captures[id]
	if !ok {
		return nil, fmt.Errorf("%w: capture %s", domain.ErrNotFound, id)
	}
	return c, nil
}

func (r *txRepo) ListCapturesByPreAuth(ctx context.Context, preauthID string) ([]*domain.Capture, error) {
	var out []*domain.Capture
	for _, c := range r.snap.Captures {
		if c.PreAuthID == preauthID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (r *txRepo) ListUnsettledCaptures(ctx context.Context) ([]*domain.Capture, error) {
	var out []*domain.Capture
	for _, c := range r.snap.Captures {
		if c.SettledBatchID == "" {
			out = append(out, c)
		}
	}
	return out, nil
}

func (r *txRepo) PutRefund(ctx context.Context, refund *domain.Refund) error {
	cp := *refund
	r.snap.Refunds[refund.ID] = &cp
	return nil
}

func (r *txRepo) GetRefund(ctx context.Context, id string) (*domain.Refund, error) {
	rf, ok := r.snap.Refunds[id]
	if !ok {
		return nil, fmt.Errorf("%w: refund %s", domain.ErrNotFound, id)
	}
	return rf, nil
}

func (r *txRepo) PutVoid(ctx context.Context, void *domain.VoidRecord) error {
	cp := *void
	r.snap.Voids[void.ID] = &cp
	return nil
}

func (r *txRepo) GetVoid(ctx context.Context, id string) (*domain.VoidRecord, error) {
	v, ok := r.snap.Voids[id]
	if !ok {
		return nil, fmt.Errorf("%w: void %s", domain.ErrNotFound, id)
	}
	return v, nil
}

func (r *txRepo) ListRefundsByPreAuth(ctx context.Context, preauthID string) ([]*domain.Refund, error) {
	var out []*domain.Refund
	for _, rf := range r.snap.Refunds {
		if rf.PreAuthID == preauthID {
			out = append(out, rf)
		}
	}
	return out, nil
}

func (r *txRepo) AppendEvent(ctx context.Context, event *domain.Event) (int64, error) {
	seq := int64(len(r.snap.Events) + 1)
	cp := *event
	cp.Seq = seq
	r.snap.Events = append(r.snap.Events, &cp)
	return seq, nil
}

func (r *txRepo) ListEvents(ctx context.Context) ([]*domain.Event, error) {
	return append([]*domain.Event(nil), r.snap.Events...), nil
}

func (r *txRepo) GetIdempotent(ctx context.Context, key string) (*domain.IdempotentRecord, error) {
	rec, ok := r.snap.Idempotency[key]
	if !ok {
		return nil, nil
	}
	return rec, nil
}

func (r *txRepo) PutIdempotent(ctx context.Context, record *domain.IdempotentRecord) (*domain.IdempotentRecord, bool, error) {
	if existing, ok := r.snap.Idempotency[record.Key]; ok {
		return existing, false, nil
	}
	cp := *record
	r.snap.Idempotency[record.Key] = &cp
	return &cp, true, nil
}

func (r *txRepo) PutBatch(ctx context.Context, batch *domain.SettlementBatch) error {
	cp := *batch
	cp.CaptureIDs = append([]string(nil), batch.CaptureIDs...)
	r.snap.Batches[batch.PeriodID] = &cp
	return nil
}

func (r *txRepo) GetBatchByPeriod(ctx context.Context, periodID string) (*domain.SettlementBatch, error) {
	b, ok := r.snap.Batches[periodID]
	if !ok {
		return nil, fmt.Errorf("%w: batch period %s", domain.ErrNotFound, periodID)
	}
	return b, nil
}

func (r *txRepo) GetBatch(ctx context.Context, id string) (*domain.SettlementBatch, error) {
	for _, b := range r.snap.Batches {
		if b.ID == id {
			return b, nil
		}
	}
	return nil, fmt.Errorf("%w: batch %s", domain.ErrNotFound, id)
}
