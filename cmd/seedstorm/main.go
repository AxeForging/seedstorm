package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/AxeForging/seedstorm/internal/app"
	"github.com/AxeForging/seedstorm/internal/safego"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Exit codes: 1 a run failed, 70 an internal error (a bug: please report it),
// 130 interrupted.
const (
	exitFailed      = 1
	exitInternal    = 70
	exitInterrupted = 130
)

func main() {
	os.Exit(run())
}

func run() int {
	// The first Ctrl+C cancels the run, so reads stop on the server (MySQL
	// queries are killed) and the summary is printed; a second one exits now.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop()
		again := make(chan os.Signal, 1)
		signal.Notify(again, os.Interrupt, syscall.SIGTERM)
		<-again
		fmt.Fprintln(os.Stderr, "interrupted again: exiting now")
		os.Exit(exitInterrupted)
	}()

	err := safego.Run("seedstorm", func() error { return app.New().Run(ctx, os.Args) })
	switch {
	case err == nil:
		return 0
	case ctx.Err() != nil:
		fmt.Fprintf(os.Stderr, "interrupted: %v\n", err)
		return exitInterrupted
	}
	var p *safego.PanicError
	if errors.As(err, &p) || errors.Is(err, tea.ErrProgramPanic) {
		fmt.Fprintf(os.Stderr, "error: %v\nThis is a bug in seedstorm; run with --log-level debug for the stack and please report it.\n", err)
		return exitInternal
	}
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	return exitFailed
}
