package store

import (
	"bytes"
	"encoding/json"
)

// Collection is a typed JSON view of one collection.
type Collection[T any] struct {
	// Name is the collection's validated store name.
	Name string
}

// Get decodes one collection value.
func (c Collection[T]) Get(tx Tx, key string) (T, bool, error) {
	var zero T
	data, ok, err := tx.Get(c.Name, key)
	if err != nil || !ok {
		return zero, ok, err
	}
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		return zero, false, err
	}
	return value, true, nil
}

// Put encodes and stores one collection value as JSON.
func (c Collection[T]) Put(tx Tx, key string, value T) error {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return err
	}
	data := bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'})
	return tx.Put(c.Name, key, data)
}

// Delete removes one collection value.
func (c Collection[T]) Delete(tx Tx, key string) error { return tx.Delete(c.Name, key) }

// Scan visits and decodes values in key order.
func (c Collection[T]) Scan(tx Tx, r Range, fn func(key string, value T) (bool, error)) error {
	return tx.Scan(c.Name, r, func(key string, data []byte) (bool, error) {
		var value T
		if err := json.Unmarshal(data, &value); err != nil {
			return false, err
		}
		return fn(key, value)
	})
}
