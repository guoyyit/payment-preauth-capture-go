package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/guoyyit/payment-preauth-capture-go/domain"
	"github.com/guoyyit/payment-preauth-capture-go/store"
)

func TestCommitAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	db, err := store.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	err = db.Update(ctx, func(repo store.Repository) error {
		if err := repo.CreateAccount(ctx, &domain.Account{
			ID: "a1", Balance: 500, Reserved: 100,
		}); err != nil {
			return err
		}
		_, err := repo.AppendEvent(ctx, &domain.Event{
			Type: domain.EventPreAuthCreated, Amount: 100,
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	db2, err := store.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db2.Read(ctx, func(repo store.Repository) error {
		a, err := repo.GetAccount(ctx, "a1")
		if err != nil {
			return err
		}
		if a.Balance != 500 || a.Reserved != 100 {
			t.Fatalf("account mismatch: %+v", a)
		}
		events, err := repo.ListEvents(ctx)
		if err != nil {
			return err
		}
		if len(events) != 1 || events[0].Seq != 1 {
			t.Fatalf("events mismatch: %+v", events)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRollbackLeavesStateUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	db, err := store.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	mustCommit := func() {
		if err := db.Update(ctx, func(repo store.Repository) error {
			return repo.CreateAccount(ctx, &domain.Account{ID: "keep", Balance: 1})
		}); err != nil {
			t.Fatal(err)
		}
	}
	mustCommit()

	rollbackErr := errors.New("boom")
	if err := db.Update(ctx, func(repo store.Repository) error {
		if err := repo.CreateAccount(ctx, &domain.Account{ID: "discard", Balance: 2}); err != nil {
			return err
		}
		_, _ = repo.AppendEvent(ctx, &domain.Event{Type: domain.EventCaptured})
		return rollbackErr
	}); !errors.Is(err, rollbackErr) {
		t.Fatalf("want rollback error, got %v", err)
	}

	if err := db.Read(ctx, func(repo store.Repository) error {
		if _, err := repo.GetAccount(ctx, "discard"); !errors.Is(err, domain.ErrAccountNotFound) {
			t.Fatalf("rolled-back account visible: %v", err)
		}
		if _, err := repo.GetAccount(ctx, "keep"); err != nil {
			t.Fatalf("committed account missing: %v", err)
		}
		events, err := repo.ListEvents(ctx)
		if err != nil {
			return err
		}
		if len(events) != 0 {
			t.Fatalf("rolled-back events visible: %d", len(events))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// File on disk must still be the pre-rollback snapshot.
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		t.Fatalf("store file damaged: %v size=%d", err, info.Size())
	}
}

func TestIdempotentInsertOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	db, err := store.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	err = db.Update(ctx, func(repo store.Repository) error {
		_, inserted, err := repo.PutIdempotent(ctx, &domain.IdempotentRecord{
			Key: "k1", OpType: domain.OpCapture, Status: domain.OpCompleted,
		})
		if err != nil || !inserted {
			t.Fatalf("first insert=%v err=%v", inserted, err)
		}
		existing, inserted, err := repo.PutIdempotent(ctx, &domain.IdempotentRecord{
			Key: "k1", OpType: domain.OpCapture,
		})
		if err != nil {
			return err
		}
		if inserted || existing.Status != domain.OpCompleted {
			t.Fatalf("second insert should return existing, inserted=%v", inserted)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
