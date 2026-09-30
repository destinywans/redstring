// Only the generated administrator in the owned testlab database is reconciled.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/store"
)

func main() {
	var input struct {
		DatabaseURL string `json:"database_url"`
		Admin       struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		} `json:"admin"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		os.Exit(2)
	}
	u, err := url.Parse(input.DatabaseURL)
	if err != nil || u.Hostname() != "127.0.0.1" || u.Port() != "15461" || u.Path != "/aiops_testlab_app" || u.User.Username() != "testlab" || input.Admin.Email != "admin@testlab.invalid" || len(input.Admin.Password) < 32 {
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, input.DatabaseURL)
	if err != nil {
		os.Exit(2)
	}
	defer db.Close()
	var present bool
	if err = db.QueryRow(ctx, "SELECT to_regclass('public.users') IS NOT NULL").Scan(&present); err != nil {
		os.Exit(2)
	}
	if present {
		var database, user string
		if err = db.QueryRow(ctx, "SELECT current_database(),current_user").Scan(&database, &user); err != nil || database != "aiops_testlab_app" || user != "testlab" {
			os.Exit(2)
		}
		tag, err := db.Exec(ctx, "UPDATE users SET password_hash=$2 WHERE email=$1 AND NOT disabled AND deleted_at IS NULL", strings.ToLower(input.Admin.Email), store.PasswordHash(input.Admin.Password))
		if err != nil || tag.RowsAffected() != 1 {
			os.Exit(2)
		}
	}
	fmt.Println("owned generated credential ready")
}
