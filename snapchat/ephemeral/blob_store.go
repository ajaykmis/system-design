package main

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// BlobStore simulates encrypted blob storage (S3/GCS in production).
// In this MVP, blobs are stored in memory.
type BlobStore struct {
	mu    sync.RWMutex
	blobs map[string]*storedBlob // ref -> blob
}

type storedBlob struct {
	data      []byte
	record    BlobRecord
}

func NewBlobStore() *BlobStore {
	return &BlobStore{blobs: make(map[string]*storedBlob)}
}

// Put stores an encrypted blob and returns a reference.
func (bs *BlobStore) Put(ref string, encryptedData []byte) error {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	bs.blobs[ref] = &storedBlob{
		data: encryptedData,
		record: BlobRecord{
			Ref:       ref,
			Size:      len(encryptedData),
			CreatedAt: time.Now(),
		},
	}
	log.Printf("[BlobStore] stored blob %s (%d bytes)", ref, len(encryptedData))
	return nil
}

// Get retrieves an encrypted blob by reference.
func (bs *BlobStore) Get(ref string) ([]byte, error) {
	bs.mu.RLock()
	defer bs.mu.RUnlock()

	blob, ok := bs.blobs[ref]
	if !ok || blob.record.Deleted {
		return nil, fmt.Errorf("blob %s not found or deleted", ref)
	}
	return blob.data, nil
}

// Delete permanently removes a blob.
func (bs *BlobStore) Delete(ref string) error {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	blob, ok := bs.blobs[ref]
	if !ok {
		return nil // already gone — idempotent
	}
	blob.record.Deleted = true
	blob.data = nil // release the bytes
	log.Printf("[BlobStore] deleted blob %s", ref)
	return nil
}

// Exists checks if a blob is present and not deleted.
func (bs *BlobStore) Exists(ref string) bool {
	bs.mu.RLock()
	defer bs.mu.RUnlock()
	blob, ok := bs.blobs[ref]
	return ok && !blob.record.Deleted
}

// Stats returns total and active blob counts.
func (bs *BlobStore) Stats() (total, active int) {
	bs.mu.RLock()
	defer bs.mu.RUnlock()
	for _, b := range bs.blobs {
		total++
		if !b.record.Deleted {
			active++
		}
	}
	return
}
