package main

import (
	"context"
	"database/sql"
	"fmt"
	"gostorage/db/migrations"
	"log"
	"os"
	"time"

	_ "github.com/lib/pq"
)

func main() {
	if len(os.Args) != 2 || (os.Args[1] != "up" && os.Args[1] != "down") {
		log.Fatal("usage: migrate up|down")
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://gostorage:gostorage@localhost:5432/gostorage_db?sslmode=disable"
	}
	db, e := sql.Open("postgres", dsn)
	if e != nil {
		log.Fatal(e)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if e = db.PingContext(ctx); e != nil {
		log.Fatal(e)
	}
	conn, e := db.Conn(ctx)
	if e != nil {
		log.Fatal(e)
	}
	defer conn.Close()
	if _, e = conn.ExecContext(ctx, `SELECT pg_advisory_lock(712409110)`); e != nil {
		log.Fatal(e)
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(712409110)`)
	if _, e = conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(version BIGINT PRIMARY KEY, dirty BOOLEAN NOT NULL)`); e != nil {
		log.Fatal(e)
	}
	var version int64
	var dirty bool
	e = conn.QueryRowContext(ctx, `SELECT version,dirty FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&version, &dirty)
	if e != nil && e != sql.ErrNoRows {
		log.Fatal(e)
	}
	if dirty {
		log.Fatal("migration state is dirty; inspect database before continuing")
	}
	direction := os.Args[1]
	if direction == "up" {
		if version >= 1 {
			fmt.Println("migrations already applied")
			return
		}
		raw, x := migrations.Files.ReadFile("000001_init.up.sql")
		if x != nil {
			log.Fatal(x)
		}
		tx, x := conn.BeginTx(ctx, nil)
		if x != nil {
			log.Fatal(x)
		}
		if _, x = tx.ExecContext(ctx, string(raw)); x == nil {
			_, x = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,dirty) VALUES(1,FALSE)`)
		}
		if x == nil {
			x = tx.Commit()
		} else {
			tx.Rollback()
		}
		if x != nil {
			log.Fatal(x)
		}
	} else {
		if version < 1 {
			fmt.Println("no migrations applied")
			return
		}
		raw, x := migrations.Files.ReadFile("000001_init.down.sql")
		if x != nil {
			log.Fatal(x)
		}
		tx, x := conn.BeginTx(ctx, nil)
		if x != nil {
			log.Fatal(x)
		}
		if _, x = tx.ExecContext(ctx, string(raw)); x == nil {
			_, x = tx.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version=1`)
		}
		if x == nil {
			x = tx.Commit()
		} else {
			tx.Rollback()
		}
		if x != nil {
			log.Fatal(x)
		}
	}
	fmt.Println("migration", direction, "complete")
}
