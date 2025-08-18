# Go POC: Pessimistic vs Optimistic DB Locking (MySQL + Docker)

This small project shows both approaches side-by-side:

- **Pessimistic locking** using `SELECT ... FOR UPDATE`
- **Optimistic locking** using a `version` column and a CAS-style `UPDATE ... WHERE version = ?`

## Prereqs

- Docker Desktop (or Docker Engine)
- Go 1.22+

## Run

```bash
cd go-locking-poc
docker compose up -d            # starts MySQL on localhost:3306 (user: root / pass: rootpw)
go mod tidy                     # pulls the MySQL driver
go run .                        # runs the demo
```

You should see logs showing:
- In the pessimistic demo, the second transaction blocks on `SELECT ... FOR UPDATE` until the first commits.
- In the optimistic demo, one goroutine wins; the other detects a version conflict and retries.

## Connection info

- DSN: `root:rootpw@tcp(127.0.0.1:3306)/lockdemo`
- The MySQL container name is `locking-mysql`.

## Clean up

```bash
docker compose down -v
```
