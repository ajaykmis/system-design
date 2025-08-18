package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"math/rand"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

const (
	dsn = "root:rootpw@tcp(127.0.0.1:3306)/lockdemo?parseTime=true&multiStatements=true&timeout=5s"
)

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func setup(db *sql.DB) error {
	schema := `
CREATE TABLE IF NOT EXISTS items (
  id INT PRIMARY KEY,
  name VARCHAR(64) NOT NULL,
  qty INT NOT NULL,
  version INT NOT NULL DEFAULT 0,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
);
INSERT INTO items (id, name, qty, version)
VALUES (1, 'widget', 100, 0)
ON DUPLICATE KEY UPDATE name=VALUES(name);
`
	_, err := db.Exec(schema)
	return err
}

func now() string {
	return time.Now().Format("15:04:05.000")
}

func pessimisticDemo(db *sql.DB) {
	log.Printf("==== Pessimistic Locking Demo (SELECT ... FOR UPDATE) ====")

	// tx1 acquires row lock, sleeps, then updates
	tx1, err := db.Begin()
	must(err)

	// Using context to show blocking behavior on tx2
	ctx := context.Background()

	log.Printf("[%s] tx1: SELECT ... FOR UPDATE", now())
	var qty1 int
	err = tx1.QueryRowContext(ctx, "SELECT qty FROM items WHERE id = 1 FOR UPDATE").Scan(&qty1)
	must(err)
	log.Printf("[%s] tx1: got qty=%d, holding lock for 2s...", now(), qty1)

	// Start tx2 concurrently
	done := make(chan struct{})
	go func() {
		defer close(done)
		tx2, err := db.Begin()
		must(err)
		var qty2 int
		log.Printf("[%s] tx2: trying SELECT ... FOR UPDATE (will block until tx1 commits)", now())
		err = tx2.QueryRowContext(ctx, "SELECT qty FROM items WHERE id = 1 FOR UPDATE").Scan(&qty2)
		must(err)
		log.Printf("[%s] tx2: acquired lock, read qty=%d", now(), qty2)
		_, err = tx2.ExecContext(ctx, "UPDATE items SET qty = qty - 5 WHERE id = 1")
		must(err)
		must(tx2.Commit())
		log.Printf("[%s] tx2: committed", now())
	}()

	time.Sleep(2 * time.Second) // simulate work while holding the lock
	_, err = tx1.ExecContext(ctx, "UPDATE items SET qty = qty - 10 WHERE id = 1")
	must(err)
	must(tx1.Commit())
	log.Printf("[%s] tx1: committed", now())

	<-done
	var qty int
	must(db.QueryRow("SELECT qty FROM items WHERE id = 1").Scan(&qty))
	log.Printf("[%s] final qty after pessimistic demo: %d (expect 100 -10 -5 = 85)", now(), qty)
}

func optimisticAttempt(db *sql.DB, id int, delta int, attempt int) (bool, error) {
	// Read current version & qty
	var version, qty int
	err := db.QueryRow("SELECT version, qty FROM items WHERE id = ?", id).Scan(&version, &qty)
	if err != nil {
		return false, err
	}

	// Simulate some work while "holding" the stale version
	time.Sleep(time.Duration(200+rand.Intn(200)) * time.Millisecond)

	// Try compare-and-swap style update
	res, err := db.Exec("UPDATE items SET qty = ?, version = version + 1 WHERE id = ? AND version = ?", qty+delta, id, version)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 0 {
		log.Printf("[%s] optimistic attempt #%d: conflict (stale version=%d). Will retry.", now(), attempt, version)
		return false, nil
	}
	log.Printf("[%s] optimistic attempt #%d: success (moved version=%d -> %d, qty change %+d)", now(), attempt, version, version+1, delta)
	return true, nil
}

func optimisticDemo(db *sql.DB) {
	log.Printf("==== Optimistic Locking Demo (version column) ====")
	// Reset the row to a known state
	_, _ = db.Exec("UPDATE items SET qty = 100, version = 0 WHERE id = 1")

	// Two workers start from the same version and try to update concurrently.
	done := make(chan struct{})
	go func() {
		defer close(done)
		attempt := 1
		for {
			ok, err := optimisticAttempt(db, 1, -7, attempt)
			must(err)
			if ok {
				return
			}
			attempt++
		}
	}()

	// Second worker
	attempt := 1
	for {
		ok, err := optimisticAttempt(db, 1, -3, attempt)
		must(err)
		if ok {
			break
		}
		attempt++
	}

	<-done
	var qty, version int
	must(db.QueryRow("SELECT qty, version FROM items WHERE id = 1").Scan(&qty, &version))
	log.Printf("[%s] final qty after optimistic demo: %d (expect 100 -7 -3 = 90), final version=%d", now(), qty, version)
}

func main() {
	log.SetFlags(0) // cleaner timestamps; we print our own

	// Connect; wait-for-MySQL retry loop
	var db *sql.DB
	var err error
	for i := 0; i < 30; i++ {
		db, err = sql.Open("mysql", dsn)
		if err == nil {
			if pingErr := db.Ping(); pingErr == nil {
				break
			} else {
				err = pingErr
			}
		}
		log.Printf("Waiting for MySQL... (%v)", err)
		time.Sleep(1 * time.Second)
	}
	must(err)
	defer db.Close()

	must(setup(db))

	// Ensure a clean baseline before demos
	_, _ = db.Exec("UPDATE items SET qty = 100, version = 0 WHERE id = 1")

	pessimisticDemo(db)
	fmt.Println()
	optimisticDemo(db)
}
