package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// WALEntry represents a single write-ahead log entry
type WALEntry struct {
	Timestamp int64  `json:"timestamp"`
	OpType    string `json:"op_type"`
	Key       string `json:"key"`
	Value     string `json:"value,omitempty"`
}

type KVStore struct {
	// concurrent map
	store map[string]string // regular map, protected by shardLocks or we could use sync.Map

	// WAL components
	walFile   *os.File
	walWriter *bufio.Writer
	walMutex  sync.Mutex

	// Fine-grained locking - shard locks by key hash
	numShards  int
	shardLocks []sync.RWMutex
}

func NewKVStore(walFilePath string) (*KVStore, error) {
	// Open or create the WAL file
	file, err := os.OpenFile(walFilePath, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}

	// Initialize shard locks
	numShards := 16
	shardLocks := make([]sync.RWMutex, numShards) // rw lock - read writer lock,\
	//  allow single writer and multipe reades for the same entry

	// initialize the WalWriter
	storage := &KVStore{
		store:      make(map[string]string),
		walWriter:  bufio.NewWriter(file),
		walFile:    file,
		numShards:  numShards,
		shardLocks: shardLocks,
	}

	// Recover from WAL if exists
	if err := storage.recoverFromWAL(walFilePath); err != nil {
		return nil, fmt.Errorf("failed to recover from WAL: %w", err)
	}

	return storage, nil
}

func (kv *KVStore) getShard(key string) int {
	return int(key[0]) % kv.numShards // simple hash function based on first byte
	// somethinf like hash() % numShards would give better distribution
}

// Write to WAL (must be called before actual operation)
func (kv *KVStore) writeWAL(entry WALEntry) error {
	kv.walMutex.Lock()
	defer kv.walMutex.Unlock()

	entry.Timestamp = time.Now().UnixNano()
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	_, err = kv.walWriter.Write(append(data, '\n'))
	if err != nil {
		return err
	}

	// Force flush for durability
	// walWriter.FSync() can be used for better performance with batching
	return kv.walWriter.Flush()
}

func (kv *KVStore) Put(key, value string) error {

	//1.  Write to WAL first
	entry := WALEntry{
		Timestamp: time.Now().UnixNano(),
		OpType:    "PUT",
		Key:       key,
		Value:     value,
	}
	err := kv.writeWAL(entry)
	if err != nil {
		// WAL write failed, do not modify in-memory state
		return err
	}

	//2. update in-memory state
	// Acquire write lock for the shard
	shard := kv.getShard(key)
	kv.shardLocks[shard].Lock()
	//make sure to unlock the lock
	defer kv.shardLocks[shard].Unlock()
	kv.store[key] = value // we have the lock
	return nil
}

// Get with fine-grained read lock
func (kv *KVStore) Get(key string) (string, bool) {
	// Acquire fine-grained read lock
	shardIdx := kv.getShard(key)
	kv.shardLocks[shardIdx].RLock()
	defer kv.shardLocks[shardIdx].RUnlock()

	// Access regular map safely under lock
	value, ok := kv.store[key]
	return value, ok
}

// delete with fine-grained write lock
func (kv *KVStore) Delete(key string) error {
	// 1. Write to WAL first
	entry := WALEntry{
		Timestamp: time.Now().UnixNano(),
		OpType:    "DELETE",
		Key:       key,
	}
	err := kv.writeWAL(entry)
	if err != nil {
		// WAL write failed, do not modify in-memory state
		return err
	}

	// 2. Update in-memory state
	shardIdx := kv.getShard(key)
	kv.shardLocks[shardIdx].Lock()
	defer kv.shardLocks[shardIdx].Unlock()

	delete(kv.store, key)
	return nil
}

// Recover from WAL on startup
func (kv *KVStore) recoverFromWAL(walPath string) error {
	file, err := os.Open(walPath)
	if os.IsNotExist(err) {
		return nil // No WAL file exists yet
	}
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	entriesRecovered := 0

	for scanner.Scan() {
		var entry WALEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue // Skip corrupted entries
		}

		// Replay operations (without writing to WAL again)
		// No locking needed during recovery - single-threaded
		switch entry.OpType {
		case "PUT":
			kv.store[entry.Key] = entry.Value
			entriesRecovered++
		case "DELETE":
			delete(kv.store, entry.Key)
			entriesRecovered++
		}
	}

	fmt.Printf("Recovery complete: %d entries replayed from WAL\n", entriesRecovered)
	return scanner.Err()
}

// Close storage and cleanup
func (kv *KVStore) Close() error {
	kv.walMutex.Lock()
	defer kv.walMutex.Unlock()

	if err := kv.walWriter.Flush(); err != nil {
		return err
	}
	return kv.walFile.Close()
}
