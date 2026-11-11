package store

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
)

func TestUpdateAndTransactions(t *testing.T) {
	t.Run("failed callback rolls back all writes", func(t *testing.T) {
		db := NewMemory()
		failure := errors.New("stop")
		err := db.Update(func(tx Tx) error {
			if err := tx.Put("test.items", "a", []byte("one")); err != nil {
				return err
			}
			if err := tx.Put("test.items", "b", []byte("two")); err != nil {
				return err
			}
			return failure
		})
		if !errors.Is(err, failure) {
			t.Fatalf("Update error = %v; want callback error", err)
		}
		if err := db.View(func(tx Tx) error {
			if _, ok, err := tx.Get("test.items", "a"); err != nil || ok {
				t.Errorf("first key visible: ok=%t err=%v", ok, err)
			}
			if _, ok, err := tx.Get("test.items", "b"); err != nil || ok {
				t.Errorf("second key visible: ok=%t err=%v", ok, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("read your writes and delete", func(t *testing.T) {
		db := NewMemory()
		if err := db.Update(func(tx Tx) error {
			if err := tx.Put("test.items", "old", []byte("old")); err != nil {
				return err
			}
			return tx.Put("test.items", "other", []byte("other"))
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.Update(func(tx Tx) error {
			if err := tx.Put("test.items", "new", []byte("new")); err != nil {
				return err
			}
			if err := tx.Delete("test.items", "old"); err != nil {
				return err
			}
			if value, ok, err := tx.Get("test.items", "new"); err != nil || !ok || string(value) != "new" {
				t.Errorf("Get new = %q, %t, %v", value, ok, err)
			}
			if _, ok, err := tx.Get("test.items", "old"); err != nil || ok {
				t.Errorf("deleted Get ok=%t err=%v", ok, err)
			}
			var keys []string
			if err := tx.Scan("test.items", Range{}, func(key string, _ []byte) (bool, error) {
				keys = append(keys, key)
				return true, nil
			}); err != nil {
				return err
			}
			if !reflect.DeepEqual(keys, []string{"new", "other"}) {
				t.Errorf("scan keys = %v", keys)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.View(func(tx Tx) error {
			if _, ok, err := tx.Get("test.items", "new"); err != nil || !ok {
				t.Errorf("committed value ok=%t err=%v", ok, err)
			}
			if _, ok, err := tx.Get("test.items", "old"); err != nil || ok {
				t.Errorf("committed deletion ok=%t err=%v", ok, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		viewDone := make(chan error, 1)
		go func() {
			viewDone <- db.View(func(tx Tx) error {
				newValue, newOK, newErr := tx.Get("test.items", "new")
				_, oldOK, oldErr := tx.Get("test.items", "old")
				if newErr != nil || !newOK || string(newValue) != "new" || oldErr != nil || oldOK {
					return errors.New("concurrent view did not see the committed update")
				}
				return nil
			})
		}()
		if err := <-viewDone; err != nil {
			t.Fatal(err)
		}
	})
	t.Run("read only and expired transactions reject writes", func(t *testing.T) {
		db := NewMemory()
		if err := db.View(func(tx Tx) error {
			if err := tx.Put("test.items", "a", []byte("x")); !errors.Is(err, ErrReadOnly) {
				t.Errorf("Put error = %v; want ErrReadOnly", err)
			}
			if _, err := tx.NextSequence("test.counter"); !errors.Is(err, ErrReadOnly) {
				t.Errorf("NextSequence error = %v; want ErrReadOnly", err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		var captured Tx
		if err := db.Update(func(tx Tx) error { captured = tx; return nil }); err != nil {
			t.Fatal(err)
		}
		if err := captured.Put("test.items", "late", []byte("x")); !errors.Is(err, ErrTxDone) {
			t.Errorf("expired Put error = %v; want ErrTxDone", err)
		}
	})
	t.Run("panic rolls back and is re-raised", func(t *testing.T) {
		db := NewMemory()
		func() {
			defer func() {
				if recover() != "panic" {
					t.Fatal("Update did not re-raise callback panic")
				}
			}()
			_ = db.Update(func(tx Tx) error {
				_ = tx.Put("test.items", "panic", []byte("x"))
				panic("panic")
			})
		}()
		if err := db.View(func(tx Tx) error {
			if _, ok, _ := tx.Get("test.items", "panic"); ok {
				t.Error("panicked write was committed")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestScan(t *testing.T) {
	t.Run("order and bounds", func(t *testing.T) {
		db := NewMemory()
		if err := db.Update(func(tx Tx) error {
			if err := tx.Put("test.keys", "a", []byte("a")); err != nil {
				return err
			}
			if err := tx.Put("test.keys", "a/1", []byte("1")); err != nil {
				return err
			}
			if err := tx.Put("test.keys", "a/2", []byte("2")); err != nil {
				return err
			}
			return tx.Put("test.keys", "b", []byte("b"))
		}); err != nil {
			t.Fatal(err)
		}
		check := func(t *testing.T, r Range, want []string) {
			t.Helper()
			var got []string
			err := db.View(func(tx Tx) error {
				return tx.Scan("test.keys", r, func(key string, _ []byte) (bool, error) { got = append(got, key); return true, nil })
			})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Scan keys = %v; want %v", got, want)
			}
		}
		check(t, Prefix("a/"), []string{"a/1", "a/2"})
		check(t, Range{Start: "a/2"}, []string{"a/2", "b"})
	})
	t.Run("missing collection is empty", func(t *testing.T) {
		db := NewMemory()
		called := false
		if err := db.View(func(tx Tx) error {
			return tx.Scan("test.missing", Range{}, func(string, []byte) (bool, error) { called = true; return true, nil })
		}); err != nil || called {
			t.Errorf("Scan missing: called=%t err=%v", called, err)
		}
	})
}

func TestExactBytesAndCollection(t *testing.T) {
	t.Run("byte values are copied and exact", func(t *testing.T) {
		value := []byte("{\"text\":\"<b>&é</b>\"}")
		db := NewMemory()
		if err := db.Update(func(tx Tx) error { return tx.Put("test.bytes", "message", value) }); err != nil {
			t.Fatal(err)
		}
		value[0] = '!'
		if err := db.View(func(tx Tx) error {
			got, ok, err := tx.Get("test.bytes", "message")
			if err != nil || !ok || !bytes.Equal(got, []byte("{\"text\":\"<b>&é</b>\"}")) {
				t.Errorf("Get = %q, %t, %v", got, ok, err)
			}
			got[0] = '!'
			again, _, _ := tx.Get("test.bytes", "message")
			if again[0] != '{' {
				t.Error("Get returned store-owned bytes")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("typed collection avoids HTML escapes", func(t *testing.T) {
		type item struct {
			Text string `json:"text"`
		}
		db := NewMemory()
		collection := Collection[item]{Name: "test.typed"}
		if err := db.Update(func(tx Tx) error { return collection.Put(tx, "one", item{Text: "<&>"}) }); err != nil {
			t.Fatal(err)
		}
		if err := db.View(func(tx Tx) error {
			data, _, err := tx.Get("test.typed", "one")
			if err != nil || string(data) != `{"text":"<&>"}` {
				t.Errorf("encoded = %s, %v", data, err)
			}
			value, ok, err := collection.Get(tx, "one")
			if err != nil || !ok || value.Text != "<&>" {
				t.Errorf("Get = %#v, %t, %v", value, ok, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestNameAndSizeRules(t *testing.T) {
	t.Run("invalid names are rejected", func(t *testing.T) {
		db := NewMemory()
		if err := db.Update(func(tx Tx) error {
			if err := tx.Put("Messages", "a", nil); !errors.Is(err, ErrInvalidName) {
				t.Errorf("uppercase name error = %v", err)
			}
			if err := tx.Put("provider", "a", nil); !errors.Is(err, ErrInvalidName) {
				t.Errorf("single name error = %v", err)
			}
			if err := tx.Put("_x.y", "a", nil); !errors.Is(err, ErrInvalidName) {
				t.Errorf("reserved name error = %v", err)
			}
			if err := tx.Put("test."+string(bytes.Repeat([]byte{'a'}, 124)), "a", nil); !errors.Is(err, ErrInvalidName) {
				t.Errorf("long name error = %v", err)
			}
			if err := tx.Put("test.items", "", nil); !errors.Is(err, ErrInvalidKey) {
				t.Errorf("empty key error = %v", err)
			}
			if err := tx.Put("test.items", string([]byte{0xff}), nil); !errors.Is(err, ErrInvalidKey) {
				t.Errorf("invalid UTF-8 key error = %v", err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("oversized value is rejected", func(t *testing.T) {
		db := NewMemory()
		if err := db.Update(func(tx Tx) error { return tx.Put("test.items", "large", make([]byte, MaxValueBytes+1)) }); !errors.Is(err, ErrTooLarge) {
			t.Errorf("Put error = %v; want ErrTooLarge", err)
		}
	})
}

func TestSequences(t *testing.T) {
	t.Run("committed values increase and failed values are reissued", func(t *testing.T) {
		db := NewMemory()
		get := func(t *testing.T, want uint64, fail bool) {
			t.Helper()
			var got uint64
			err := db.Update(func(tx Tx) error {
				value, err := tx.NextSequence("test.counter")
				got = value
				if fail {
					return errors.New("rollback")
				}
				return err
			})
			if fail && err == nil {
				t.Fatal("failed update returned nil")
			}
			if !fail && err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("sequence = %d; want %d", got, want)
			}
		}
		get(t, 1, false)
		get(t, 2, false)
		get(t, 3, false)
		get(t, 4, true)
		get(t, 4, false)
	})
}

func TestConcurrentSequences(t *testing.T) {
	t.Run("updates serialize while views run", func(t *testing.T) {
		db := NewMemory()
		type result struct {
			value uint64
			err   error
		}
		results := make(chan result, 4)
		viewResult := make(chan error, 1)
		commit := func() {
			var value uint64
			err := db.Update(func(tx Tx) error { var err error; value, err = tx.NextSequence("test.counter"); return err })
			results <- result{value: value, err: err}
		}
		go commit()
		go commit()
		go commit()
		go commit()
		go func() {
			viewResult <- db.View(func(tx Tx) error { _, _, err := tx.Get("test.counter", "missing"); return err })
		}()
		first := <-results
		second := <-results
		third := <-results
		fourth := <-results
		if first.err != nil || second.err != nil || third.err != nil || fourth.err != nil {
			t.Fatalf("sequence update errors = %v, %v, %v, %v", first.err, second.err, third.err, fourth.err)
		}
		got := map[uint64]bool{first.value: true, second.value: true, third.value: true, fourth.value: true}
		want := map[uint64]bool{1: true, 2: true, 3: true, 4: true}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("sequence values = %v; want %v", got, want)
		}
		if err := <-viewResult; err != nil {
			t.Errorf("View error = %v", err)
		}
	})
}
