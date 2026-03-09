package main

import (
	"context"
	"fmt"
	"os"

	"github.com/AxeForging/seedstorm/internal/app"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	if err := app.New().Run(context.Background(), os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
