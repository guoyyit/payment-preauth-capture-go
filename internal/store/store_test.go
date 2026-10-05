package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/guoyyit/payment-preauth-capture-go/internal/domain"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snap.json")
	st, snap, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	snap.Accounts["a"] = &domain.Account{ID: "a", Balance: 100, Frozen: 30}
	snap.PreAuths["p"] = &domain.PreAuth{
		ID: "p", AccountID: "a", Amount: 30, Status: domain.AuthActive,
	}
	if err := st.Save(snap); err != nil {
		t.Fatal(err)
	}

	_, loaded, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Accounts["a"].Balance != 100 || loaded.Accounts["a"].Frozen != 30 {
		t.Fatalf("round trip mismatch: %+v", loaded.Accounts["a"])
	}
}

func TestRecover_RecomputesFrozenAndRollsBackOpenBatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snap.json")
	st, snap, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟不一致快照：账户冻结额与预授权不匹配，且存在 open 批次。
	snap.Accounts["a"] = &domain.Account{ID: "a", Balance: 1000, Frozen: 999}
	snap.PreAuths["p"] = &domain.PreAuth{ID: "p", AccountID: "a", Amount: 500, Captured: 200, Status: domain.AuthActive}
	snap.Captures["c"] = &domain.Capture{ID: "c", AuthID: "p", Amount: 200, Settled: false, BatchID: "b"}
	snap.Batches["b"] = &domain.SettlementBatch{ID: "b", Status: domain.BatchOpen, CaptureIDs: []string{"c"}, TotalAmount: 200}
	if err := st.Save(snap); err != nil {
		t.Fatal(err)
	}

	_, rec, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	// Remaining = 500-200 = 300 -> 冻结额重算。
	if rec.Accounts["a"].Frozen != 300 {
		t.Fatalf("frozen recompute wrong: %d", rec.Accounts["a"].Frozen)
	}
	if _, ok := rec.Batches["b"]; ok {
		t.Fatalf("open batch must be rolled back")
	}
	if c := rec.Captures["c"]; c.BatchID != "" || c.Settled {
		t.Fatalf("capture must be released: %+v", c)
	}
}

func TestCorruptSnapshot_ReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snap.json")
	st, _, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Save(&domain.Snapshot{Accounts: map[string]*domain.Account{}}); err != nil {
		t.Fatal(err)
	}
	// 追加非法 JSON 制造损坏。
	if err := appendBytes(path, []byte("{not-json")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := New(path); err == nil {
		t.Fatalf("corrupt snapshot must fail to load")
	}
}

func appendBytes(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(b)
	return err
}
