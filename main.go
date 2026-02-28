package main

import (
	"bufio"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/aclindsa/ofxgo"
	_ "github.com/mattn/go-sqlite3"
	"github.com/tealeg/xlsx"
)

type Tx struct {
	Date        time.Time
	Amount      float64
	Description string
	Category    string
}

var defaultRules = map[string]string{
	"IFOOD":   "Alimentação",
	"UBER":    "Transporte",
	"99":      "Transporte",
	"NETFLIX": "Assinaturas",
	"SPOTIFY": "Assinaturas",
	"AMAZON":  "Compras",
}

func main() {
	var dir string
	if len(os.Args) < 2 {
		dir = "./ofxs"
	} else {
		dir = os.Args[1]
	}

	db := mustDB()
	defer db.Close()

	files := listOFX(dir)

	fileCh := make(chan string)
	txCh := make(chan Tx, 1000)

	workers := runtime.NumCPU()

	// -------- workers de parsing --------
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range fileCh {
				parseFile(path, txCh)
			}
		}()
	}

	// -------- writer único (sqlite) --------
	var unknown []Tx
	done := make(chan struct{})

	go func() {
		for tx := range txCh {
			cat := matchRule(db, tx.Description)

			if cat == "" {
				unknown = append(unknown, tx)
				continue
			}

			saveTx(db, tx, cat)
		}
		close(done)
	}()

	// envia arquivos
	for _, f := range files {
		fileCh <- f
	}
	close(fileCh)

	wg.Wait()
	close(txCh)
	<-done

	// pergunta categorias desconhecidas
	if len(unknown) > 0 {
		resolveUnknown(db, unknown)
	}

	backupSpreadsheet("gastos.xlsx")
	generateSpreadsheet(db, "gastos.xlsx")

	fmt.Println("✔ importação concluída")
}

func listOFX(dir string) []string {
	var files []string
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if strings.HasSuffix(strings.ToLower(path), ".ofx") {
			files = append(files, path)
		}
		return nil
	})
	return files
}

func parseFile(path string, txCh chan<- Tx) {
	f, _ := os.Open(path)
	defer f.Close()

	resp, _ := ofxgo.ParseResponse(f)

	// todo add Bank processor too?
	for _, stmt := range resp.CreditCard {

		t := stmt.(*ofxgo.CCStatementResponse).BankTranList

		for _, l := range t.Transactions {

			amount, _ := l.TrnAmt.Rat.Float64()

			txCh <- Tx{
				Date:        l.DtPosted.Time,
				Amount:      amount,
				Description: strings.ToUpper(string(l.Name)),
			}
		}
	}

	fmt.Println("parsed:", path)
}

func mustDB() *sql.DB {
	db, _ := sql.Open("sqlite3", "finance.db")

	db.Exec(`
	CREATE TABLE IF NOT EXISTS transactions(
		id INTEGER PRIMARY KEY,
		date TEXT,
		amount REAL,
		description TEXT,
		category TEXT
	);

	CREATE TABLE IF NOT EXISTS rules(
		keyword TEXT UNIQUE,
		category TEXT
	);
	`)

	for k, v := range defaultRules {
		db.Exec("INSERT OR IGNORE INTO rules(keyword, category) VALUES(?,?)", k, v)
	}

	return db
}

func matchRule(db *sql.DB, desc string) string {
	rows, _ := db.Query("SELECT keyword, category FROM rules")
	defer rows.Close()

	for rows.Next() {
		var k, c string
		rows.Scan(&k, &c)
		if strings.Contains(desc, k) {
			return c
		}
	}
	return ""
}

func saveTx(db *sql.DB, tx Tx, cat string) {
	db.Exec(
		"INSERT INTO transactions(date, amount, description, category) VALUES(?,?,?,?)",
		tx.Date.Format("2006-01-02"), tx.Amount, tx.Description, cat,
	)
}

func resolveUnknown(db *sql.DB, list []Tx) {
	reader := bufio.NewReader(os.Stdin)

	for _, tx := range list {
		fmt.Printf("\n%s | %.2f | %s\nCategoria: ",
			tx.Date.Format("2006-01-02"), tx.Amount, tx.Description)

		cat, _ := reader.ReadString('\n')
		cat = strings.TrimSpace(cat)

		saveTx(db, tx, cat)

		db.Exec("INSERT OR IGNORE INTO rules(keyword, category) VALUES(?,?)",
			firstWord(tx.Description), cat)
	}
}

func firstWord(s string) string {
	return strings.Split(s, " ")[0]
}

func backupSpreadsheet(file string) {
	if _, err := os.Stat(file); err == nil {
		backup := fmt.Sprintf("%s.%s.bak", file, time.Now().Format("20060102_150405"))
		os.Rename(file, backup)
	}
}

func generateSpreadsheet(db *sql.DB, file string) {
	rows, _ := db.Query("SELECT date, amount, description, category FROM transactions")
	defer rows.Close()

	months := map[string][]Tx{}

	for rows.Next() {
		var d string
		var t Tx
		rows.Scan(&d, &t.Amount, &t.Description, &t.Category)
		t.Date, _ = time.Parse("2006-01-02", d)

		k := t.Date.Format("2006-01")
		months[k] = append(months[k], t)
	}

	xl := xlsx.NewFile()

	for m, txs := range months {
		s, _ := xl.AddSheet(m)

		r := s.AddRow()
		r.WriteSlice(&[]string{"Data", "Valor", "Descrição", "Categoria"}, -1)

		for _, tx := range txs {
			r := s.AddRow()
			r.WriteSlice(&[]interface{}{
				tx.Date.Format("2006-01-02"),
				tx.Amount,
				tx.Description,
				tx.Category,
			}, -1)
		}
	}

	xl.Save(file)
}
