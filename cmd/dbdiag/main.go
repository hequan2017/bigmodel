package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	db, err := sql.Open("pgx", os.Getenv("DATABASE_URL"))
	if err != nil { fmt.Println("open:", err); return }
	defer db.Close()

	var exts []string
	rows, err := db.Query(`SELECT extname FROM pg_extension ORDER BY extname`)
	if err != nil { fmt.Println("q1:", err); return }
	for rows.Next() { var s string; rows.Scan(&s); exts = append(exts, s) }
	rows.Close()
	fmt.Println("扩展(前):", exts)

	if _, err = db.Exec(`CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
		fmt.Println("CREATE EXTENSION 错误:", err)
	} else {
		fmt.Println("CREATE EXTENSION 成功")
	}

	rows, err = db.Query(`SELECT extname FROM pg_extension ORDER BY extname`)
	if err != nil { fmt.Println("q2:", err); return }
	exts = nil
	for rows.Next() { var s string; rows.Scan(&s); exts = append(exts, s) }
	rows.Close()
	fmt.Println("扩展(后):", exts)

	var schema string
	if err = db.QueryRow(`SHOW search_path`).Scan(&schema); err != nil { fmt.Println("sp:", err); return }
	fmt.Println("search_path:", schema)

	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS vtest (v public.vector)`); err != nil {
		fmt.Println("CREATE TABLE 错误:", err)
	} else {
		fmt.Println("CREATE TABLE public.vector 成功")
		db.Exec(`DROP TABLE vtest`)
	}
}
