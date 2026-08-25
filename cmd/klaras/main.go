// Command klaras is the Klaras Library server and its maintenance CLI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/Karlmit/Klaras-Library/internal/auth"
	"github.com/Karlmit/Klaras-Library/internal/calibre"
	"github.com/Karlmit/Klaras-Library/internal/config"
	"github.com/Karlmit/Klaras-Library/internal/covers"
	"github.com/Karlmit/Klaras-Library/internal/devseed"
	"github.com/Karlmit/Klaras-Library/internal/filestore"
	"github.com/Karlmit/Klaras-Library/internal/httpapi"
	"github.com/Karlmit/Klaras-Library/internal/ingest"
	"github.com/Karlmit/Klaras-Library/internal/jobs"
	"github.com/Karlmit/Klaras-Library/internal/kepub"
	"github.com/Karlmit/Klaras-Library/internal/library"
	"github.com/Karlmit/Klaras-Library/internal/provider"
	"github.com/Karlmit/Klaras-Library/internal/store"
)

// version is stamped at build time via -ldflags.
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "klaras: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "serve":
		return cmdServe()
	case "migrate":
		return cmdMigrate()
	case "dev-seed":
		return cmdDevSeed()
	case "import":
		return cmdImport()
	case "backfill-covers":
		return cmdBackfillCovers()
	case "reorganize", "reorganise":
		return cmdReorganize()
	case "revert-moves":
		return cmdRevertMoves()
	case "doctor":
		return cmdDoctor()
	case "users":
		return cmdUsers()
	case "passwd":
		return cmdPasswd()
	case "version":
		fmt.Println("klaras", version)
		return nil
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `klaras -- Klaras Library

Usage:
  klaras serve            Run the web server (default)
  klaras migrate [up|down|status]
                          Apply or inspect database migrations
  klaras import --calibre-library PATH [--dry-run] [--purge]
                          Import an existing Calibre library
  klaras backfill-covers [--force] [--wait]
                          Queue thumbnail generation for the whole library
  klaras reorganize [--dry-run] [--out FILE]
                          Bring the file tree into line with the path template.
                          DESTRUCTIVE: always review --dry-run output first.
  klaras revert-moves --since TIME [--dry-run]
                          Undo file moves made since TIME, using the journal
  klaras users            List accounts
  klaras passwd USERNAME  Set an account's password
  klaras doctor           Check the library for problems (read-only)
  klaras dev-seed --books N
                          Replace the library with N synthetic books, for
                          benchmarking. DESTRUCTIVE -- never run against a
                          real library.
  klaras version          Print the build version

Configuration is read from KLARAS_-prefixed environment variables;
see .env.example for the full list.
`)
}

func cmdServe() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg)
	log.Info("starting klaras", "version", version, "library_root", cfg.LibraryRoot)

	// Signal-aware root context: Ctrl-C or SIGTERM cancels everything below.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := store.Migrate(ctx, cfg.DatabaseURL, log); err != nil {
		return err
	}

	db, err := store.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns, log)
	if err != nil {
		return err
	}
	defer db.Close()

	lib := library.New(db.Pool)
	authSvc := auth.NewService(db.Pool)
	coverSvc := covers.New(cfg.LibraryRoot, cfg.CacheDir)
	kepubSvc := kepub.New(cfg.LibraryRoot, cfg.CacheDir)
	files := filestore.New(cfg.LibraryRoot, filestore.Template{
		WithSeries: cfg.PathTemplateSeries,
		Plain:      cfg.PathTemplatePlain,
		File:       cfg.FileTemplate,
	}, db.Pool, log)

	// Replay any file move interrupted by a previous shutdown before serving:
	// until this runs, the catalogue may point at a path nothing is behind.
	if rep, err := files.Reconcile(ctx); err != nil {
		log.Error("file reconciliation failed", "err", err)
	} else if rep.Examined > 0 {
		log.Warn("recovered interrupted file operations",
			"examined", rep.Examined, "completed", rep.Completed,
			"rolled_back", rep.RolledBack, "duplicates", rep.Duplicates,
			"lost", rep.Lost, "failed", rep.Failed)
	}
	queue := jobs.New(db.Pool, log)

	// A worker that died mid-job leaves its row locked, and the dedupe key
	// would then block the work ever being re-queued.
	if n, err := queue.ReclaimStuck(ctx, 15*time.Minute); err != nil {
		log.Warn("could not reclaim stuck jobs", "err", err)
	} else if n > 0 {
		log.Info("reclaimed jobs abandoned by a previous run", "jobs", n)
	}

	// Facet counts are materialised; make sure they exist before serving.
	if _, err := lib.RefreshFacets(ctx, false); err != nil {
		log.Warn("initial facet refresh failed", "err", err)
	}

	srvHandler := httpapi.New(httpapi.Deps{
		Config: cfg, DB: db, Library: lib, Auth: authSvc,
		Covers: coverSvc, Kepub: kepubSvc, Files: files,
		Providers: provider.NewSet("swe"),
		Queue:     queue, Log: log, Version: version,
	})

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srvHandler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		// No WriteTimeout: book downloads and KEPUB streams can legitimately
		// run long. Per-handler timeouts guard the fast paths instead.
	}

	// Background workers, all tied to the root context so shutdown stops them.
	go queue.Run(ctx, jobs.KindThumbnail, cfg.CoverWorkers, 5*time.Second, coverSvc.Handler())
	go queue.Run(ctx, jobs.KindKepub, cfg.ConvertWorkers, 5*time.Second, kepubSvc.Handler())
	// One worker for moves: the file store serialises per book anyway, and a
	// single mover keeps the tree easy to reason about during a bulk edit.
	go queue.Run(ctx, jobs.KindFileMove, 1, 3*time.Second, files.Handler())

	// Watch folder. The periodic sweep is the reliable half: inotify does not
	// fire for files written to an Unraid share by another machine.
	if cfg.IngestDir != "" {
		go ingest.New(cfg.IngestDir, db.Pool, files, coverSvc, queue, log).
			Run(ctx, 60*time.Second)
	}
	go lib.RunFacetRefresher(ctx, 30*time.Second, log)

	// Prune expired lockout entries, so a long guessing run against random
	// usernames cannot grow the limiter's map without bound.
	stopSweeper := make(chan struct{})
	defer close(stopSweeper)
	go srvHandler.Limiter().RunSweeper(stopSweeper, 5*time.Minute)

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("server: %w", err)
	case <-ctx.Done():
		log.Info("shutdown signal received", "grace", cfg.ShutdownGrace)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	log.Info("stopped cleanly")
	return nil
}

func cmdMigrate() error {
	sub := "up"
	if len(os.Args) > 2 {
		sub = os.Args[2]
	}
	dsn := os.Getenv("KLARAS_DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("KLARAS_DATABASE_URL is required")
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx := context.Background()

	switch sub {
	case "up":
		return store.Migrate(ctx, dsn, log)
	case "down":
		return store.MigrateDown(ctx, dsn, log)
	case "status":
		v, err := store.SchemaVersion(ctx, dsn)
		if err != nil {
			return err
		}
		fmt.Printf("schema version: %d\n", v)
		return nil
	default:
		return fmt.Errorf("unknown migrate subcommand %q (want up, down or status)", sub)
	}
}

func cmdImport() error {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	lib := fs.String("calibre-library", "", "path to the Calibre library directory (containing metadata.db)")
	appDB := fs.String("calibre-web-db", "", "path to calibre-web's app.db (users, shelves, Kobo state)")
	dryRun := fs.Bool("dry-run", false, "read and validate everything, then roll back")
	purge := fs.Bool("purge", false, "replace any books already in the destination")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	if *lib == "" {
		return fmt.Errorf("--calibre-library is required")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg)
	ctx := context.Background()

	src, err := calibre.OpenSource(*lib)
	if err != nil {
		return err
	}
	defer src.Close()

	stats, err := src.Stat()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\nSource: %s\n", *lib)
	fmt.Fprintf(os.Stderr, "  books %d, authors %d, series %d, tags %d, publishers %d\n",
		stats.Books, stats.Authors, stats.Series, stats.Tags, stats.Publishers)
	fmt.Fprintf(os.Stderr, "  files %d, comments %d, identifiers %d, custom columns %d\n",
		stats.Files, stats.Comments, stats.Identifiers, stats.CustomCols)
	fmt.Fprintf(os.Stderr, "  formats: %s\n", formatCounts(stats.FormatCounts))
	fmt.Fprintf(os.Stderr, "  languages: %s\n\n", topLanguages(stats.Languages, 5))

	if err := store.Migrate(ctx, cfg.DatabaseURL, log); err != nil {
		return err
	}
	db, err := store.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns, log)
	if err != nil {
		return err
	}
	defer db.Close()

	// Both halves share one transaction, so --dry-run exercises the app.db
	// import too, and a real run never leaves a committed library with no
	// shelves or users attached.
	var adb *calibre.AppDB
	if *appDB != "" {
		adb, err = calibre.OpenAppDB(*appDB)
		if err != nil {
			return err
		}
		defer adb.Close()
	}

	res, ares, err := calibre.ImportAll(ctx, db.Pool, src, adb,
		calibre.Options{DryRun: *dryRun, Purge: *purge}, log)
	if err != nil {
		return err
	}

	if ares != nil {
		fmt.Fprintf(os.Stderr, "\ncalibre-web state:\n")
		fmt.Fprintf(os.Stderr, "  users        %d (skipped %d)\n", ares.Users, ares.SkippedUser)
		fmt.Fprintf(os.Stderr, "  shelves      %d\n", ares.Shelves)
		fmt.Fprintf(os.Stderr, "  shelf books  %d (dropped %d pointing at missing books)\n",
			ares.ShelfBooks, ares.OrphanLinks)
		fmt.Fprintf(os.Stderr, "  kobo tokens  %d\n", ares.KoboTokens)
		fmt.Fprintf(os.Stderr, "  read states  %d\n", ares.ReadStates)
		if ares.Users > 0 {
			fmt.Fprintf(os.Stderr, "  NOTE: imported accounts have no usable password "+
				"(calibre-web scrypt hashes cannot be converted to argon2id).\n"+
				"        Set one for each with:  klaras passwd USERNAME\n")
		}
	} else {
		fmt.Fprintf(os.Stderr, "\nNo --calibre-web-db given, so no users, shelves or "+
			"Kobo tokens were imported.\n")
	}

	fmt.Fprintf(os.Stderr, "\nImported in %s%s\n", res.Elapsed.Round(time.Millisecond),
		map[bool]string{true: "", false: " (DRY RUN - rolled back)"}[res.Committed])
	fmt.Fprintf(os.Stderr, "  books        %d\n", res.Books)
	fmt.Fprintf(os.Stderr, "  authors      %d\n", res.Authors)
	fmt.Fprintf(os.Stderr, "  series       %d\n", res.Series)
	fmt.Fprintf(os.Stderr, "  tags         %d\n", res.Tags)
	fmt.Fprintf(os.Stderr, "  publishers   %d\n", res.Publishers)
	fmt.Fprintf(os.Stderr, "  book_authors %d\n", res.BookAuthors)
	fmt.Fprintf(os.Stderr, "  book_tags    %d\n", res.BookTags)
	fmt.Fprintf(os.Stderr, "  identifiers  %d\n", res.Identifiers)
	fmt.Fprintf(os.Stderr, "  files        %d\n", res.Files)
	for k, v := range res.Skipped {
		fmt.Fprintf(os.Stderr, "  SKIPPED %-22s %d\n", k, v)
	}
	if len(res.Issues) > 0 {
		fmt.Fprintf(os.Stderr, "\nFlagged for review (imported faithfully, not auto-corrected):\n")
		for _, i := range res.Issues {
			fmt.Fprintf(os.Stderr, "  %-26s %6d  %s\n", i.Reason, i.Count, i.Note)
		}
	}
	return nil
}

func formatCounts(m map[string]int64) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return m[keys[i]] > m[keys[j]] })
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, " ")
}

func topLanguages(m map[string]int64, n int) string {
	keys := make([]string, 0, len(m))
	var total int64
	for k, v := range m {
		keys = append(keys, k)
		total += v
	}
	sort.Slice(keys, func(i, j int) bool { return m[keys[i]] > m[keys[j]] })
	if len(keys) > n {
		keys = keys[:n]
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		pct := 0.0
		if total > 0 {
			pct = 100 * float64(m[k]) / float64(total)
		}
		parts = append(parts, fmt.Sprintf("%s=%d (%.1f%%)", k, m[k], pct))
	}
	return strings.Join(parts, " ")
}

func cmdBackfillCovers() error {
	fs := flag.NewFlagSet("backfill-covers", flag.ExitOnError)
	force := fs.Bool("force", false, "regenerate thumbnails that already exist")
	wait := fs.Bool("wait", false, "stay running and process the queue instead of only filling it")
	workers := fs.Int("workers", 0, "worker count when --wait is set (default: KLARAS_COVER_WORKERS)")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns, log)
	if err != nil {
		return err
	}
	defer db.Close()

	svc := covers.New(cfg.LibraryRoot, cfg.CacheDir)
	queue := jobs.New(db.Pool, log)

	queued, skipped, err := covers.Backfill(ctx, db.Pool, svc, queue, *force, log)
	if err != nil {
		return err
	}
	log.Info("cover backfill queued", "queued", queued, "already_present", skipped)

	if !*wait {
		fmt.Fprintf(os.Stderr, "queued %d covers (%d already present). "+
			"The running server will work through them; "+
			"re-run with --wait to process them here instead.\n", queued, skipped)
		return nil
	}

	n := *workers
	if n == 0 {
		n = cfg.CoverWorkers
	}
	go queue.Run(ctx, jobs.KindThumbnail, n, 2*time.Second, svc.Handler())

	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			st, err := queue.Stats(ctx, jobs.KindThumbnail)
			if err != nil {
				return err
			}
			log.Info("cover progress", "pending", st.Pending, "running", st.Running,
				"done", st.Done, "failed", st.Failed)
			if st.Pending == 0 && st.Running == 0 {
				log.Info("cover backfill complete", "done", st.Done, "failed", st.Failed)
				return nil
			}
		}
	}
}

func cmdReorganize() error {
	fs := flag.NewFlagSet("reorganize", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "print the full plan without touching anything")
	outPath := fs.String("out", "", "write the plan to this file instead of stderr")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns, log)
	if err != nil {
		return err
	}
	defer db.Close()

	files := filestore.New(cfg.LibraryRoot, filestore.Template{
		WithSeries: cfg.PathTemplateSeries,
		Plain:      cfg.PathTemplatePlain,
		File:       cfg.FileTemplate,
	}, db.Pool, log)

	var out io.Writer = os.Stderr
	if *outPath != "" {
		f, err := os.Create(*outPath)
		if err != nil {
			return err
		}
		defer f.Close()
		out = f
	}
	if !*dryRun && !*yes {
		return fmt.Errorf("reorganize moves files on disk. " +
			"Run with --dry-run first to review the plan, then re-run with --yes")
	}

	rep, err := files.Reorganize(ctx, *dryRun, out, log)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\n%s in %s\n",
		map[bool]string{true: "DRY RUN (nothing was changed)", false: "Reorganised"}[*dryRun],
		rep.Elapsed.Round(time.Millisecond))
	fmt.Fprintf(os.Stderr, "  examined   %d\n", rep.Examined)
	fmt.Fprintf(os.Stderr, "  to move    %d\n", rep.Planned)
	fmt.Fprintf(os.Stderr, "  already ok %d\n", rep.Unchanged)
	if !*dryRun {
		fmt.Fprintf(os.Stderr, "  moved      %d\n", rep.Applied)
	}
	fmt.Fprintf(os.Stderr, "  failed     %d\n", rep.Failed)
	return nil
}

func cmdRevertMoves() error {
	fs := flag.NewFlagSet("revert-moves", flag.ExitOnError)
	sinceStr := fs.String("since", "", "undo moves completed at or after this time "+
		"(RFC3339, e.g. 2026-08-25T12:00:00Z, or a duration like 2h)")
	dryRun := fs.Bool("dry-run", false, "list what would be undone, without touching anything")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	if *sinceStr == "" {
		return fmt.Errorf("--since is required, so a revert can never walk back " +
			"further than you intended")
	}

	var since time.Time
	if d, err := time.ParseDuration(*sinceStr); err == nil {
		since = time.Now().Add(-d)
	} else if t, err := time.Parse(time.RFC3339, *sinceStr); err == nil {
		since = t
	} else {
		return fmt.Errorf("--since must be RFC3339 (2026-08-25T12:00:00Z) or a duration (2h)")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns, log)
	if err != nil {
		return err
	}
	defer db.Close()

	files := filestore.New(cfg.LibraryRoot, filestore.Template{
		WithSeries: cfg.PathTemplateSeries,
		Plain:      cfg.PathTemplatePlain,
		File:       cfg.FileTemplate,
	}, db.Pool, log)

	if !*dryRun && !*yes {
		return fmt.Errorf("revert-moves moves files on disk. " +
			"Run with --dry-run first, then re-run with --yes")
	}

	var out io.Writer
	if *dryRun {
		out = os.Stderr
	}
	rep, err := files.Revert(ctx, since, *dryRun, out, log)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\n%s in %s\n",
		map[bool]string{true: "DRY RUN (nothing was changed)", false: "Reverted"}[*dryRun],
		rep.Elapsed.Round(time.Millisecond))
	fmt.Fprintf(os.Stderr, "  moves since %s   %d\n", since.Format(time.RFC3339), rep.Candidates)
	if !*dryRun {
		fmt.Fprintf(os.Stderr, "  undone                            %d\n", rep.Reverted)
	}
	fmt.Fprintf(os.Stderr, "  skipped (already back, or occupied) %d\n", rep.Skipped)
	fmt.Fprintf(os.Stderr, "  failed                              %d\n", rep.Failed)
	return nil
}

func cmdUsers() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg)
	ctx := context.Background()

	db, err := store.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns, log)
	if err != nil {
		return err
	}
	defer db.Close()

	rows, err := db.Pool.Query(ctx, `
		SELECT u.id, u.username, COALESCE(u.email,''), u.role, u.is_active,
		       u.password_reset_required,
		       (SELECT count(*) FROM shelves s WHERE s.user_id = u.id),
		       (SELECT count(*) FROM shelves s WHERE s.user_id = u.id AND s.kobo_sync),
		       (SELECT count(*) FROM kobo_auth_tokens k WHERE k.user_id = u.id)
		FROM users u ORDER BY u.id`)
	if err != nil {
		return err
	}
	defer rows.Close()

	fmt.Printf("%-4s %-18s %-26s %-8s %-8s %-7s %-6s %s\n",
		"ID", "USERNAME", "EMAIL", "ROLE", "ACTIVE", "SHELVES", "KOBO", "PASSWORD")
	needsPassword := 0
	for rows.Next() {
		var (
			id, shelves, koboShelves, tokens int64
			username, email, role            string
			active, mustReset                bool
		)
		if err := rows.Scan(&id, &username, &email, &role, &active, &mustReset,
			&shelves, &koboShelves, &tokens); err != nil {
			return err
		}
		pw := "set"
		if mustReset {
			pw = "NOT SET -- cannot log in"
			needsPassword++
		}
		kobo := fmt.Sprintf("%d/%d", koboShelves, tokens)
		fmt.Printf("%-4d %-18s %-26s %-8s %-8v %-7d %-6s %s\n",
			id, username, email, role, active, shelves, kobo, pw)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\nKOBO column is synced-shelves/tokens.\n")
	if needsPassword > 0 {
		fmt.Fprintf(os.Stderr, "\n%d account(s) came from calibre-web and have no usable password.\n"+
			"calibre-web hashes cannot be converted, so set one for each:\n"+
			"    klaras passwd USERNAME\n", needsPassword)
	}
	return nil
}

func cmdPasswd() error {
	fs := flag.NewFlagSet("passwd", flag.ExitOnError)
	password := fs.String("password", "", "the new password; omit to be prompted")
	role := fs.String("role", "", "also set the role (admin, editor or reader)")

	// Go's flag package stops parsing at the first positional argument, so
	// `passwd USERNAME --password X` would silently ignore the flags. That is
	// the order anyone would type, so pull the username out first.
	username, rest := splitPositional(os.Args[2:])
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if username == "" || fs.NArg() > 0 {
		return fmt.Errorf("usage: klaras passwd USERNAME [--password PW] [--role ROLE]")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg)
	ctx := context.Background()

	db, err := store.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns, log)
	if err != nil {
		return err
	}
	defer db.Close()

	var id int64
	if err := db.Pool.QueryRow(ctx,
		`SELECT id FROM users WHERE lower(username)=lower($1)`, username).Scan(&id); err != nil {
		return fmt.Errorf("no account called %q (try: klaras users)", username)
	}

	pw := *password
	if pw == "" {
		// Read from the terminal without echoing.
		fmt.Fprintf(os.Stderr, "New password for %s: ", username)
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return fmt.Errorf("could not read the password: %w "+
				"(no terminal? use --password)", err)
		}
		pw = string(b)
	}

	svc := auth.NewService(db.Pool)
	if err := svc.SetPassword(ctx, id, pw); err != nil {
		return err
	}
	if *role != "" {
		switch *role {
		case auth.RoleAdmin, auth.RoleEditor, auth.RoleReader:
		default:
			return fmt.Errorf("unknown role %q", *role)
		}
		if _, err := db.Pool.Exec(ctx,
			`UPDATE users SET role=$2, updated_at=now() WHERE id=$1`, id, *role); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "Password set for %s. They can sign in now.\n", username)
	return nil
}

// splitPositional returns the first non-flag argument and everything else, so
// flags may appear on either side of it.
func splitPositional(args []string) (positional string, rest []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			rest = append(rest, a)
			// A flag written as "--password X" consumes the next argument;
			// "--password=X" does not.
			if !strings.Contains(a, "=") && i+1 < len(args) &&
				!strings.HasPrefix(args[i+1], "-") {
				i++
				rest = append(rest, args[i])
			}
			continue
		}
		if positional == "" {
			positional = a
			continue
		}
		rest = append(rest, a)
	}
	return positional, rest
}

func cmdDoctor() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg)
	ctx := context.Background()

	db, err := store.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns, log)
	if err != nil {
		return err
	}
	defer db.Close()

	type check struct {
		name string
		q    string
	}
	// Read-only by design: doctor reports, it never repairs. Repair decisions
	// belong to a human looking at the report.
	checks := []check{
		{"books", "SELECT count(*) FROM books"},
		{"books flagged for review", "SELECT count(*) FROM books WHERE needs_review"},
		{"books with no files", "SELECT count(*) FROM books b WHERE NOT EXISTS (SELECT 1 FROM book_files f WHERE f.book_id=b.id)"},
		{"books with no EPUB", "SELECT count(*) FROM books b WHERE NOT EXISTS (SELECT 1 FROM book_files f WHERE f.book_id=b.id AND f.format='EPUB')"},
		{"books needing KEPUB", "SELECT count(*) FROM books b WHERE EXISTS (SELECT 1 FROM book_files f WHERE f.book_id=b.id AND f.format='EPUB') AND NOT EXISTS (SELECT 1 FROM book_files f WHERE f.book_id=b.id AND f.format='KEPUB')"},
		{"unfinished file operations", "SELECT count(*) FROM file_operations WHERE state IN ('pending','staged')"},
		{"failed file operations", "SELECT count(*) FROM file_operations WHERE state='failed'"},
		{"failed jobs", "SELECT count(*) FROM jobs WHERE state='failed'"},
		{"pending jobs", "SELECT count(*) FROM jobs WHERE state='pending'"},
		{"kobo-synced shelves", "SELECT count(*) FROM shelves WHERE kobo_sync"},
		{"books on kobo shelves", "SELECT count(DISTINCT sb.book_id) FROM shelf_books sb JOIN shelves s ON s.id=sb.shelf_id WHERE s.kobo_sync"},
		{"users needing a password reset", "SELECT count(*) FROM users WHERE password_reset_required"},
	}
	fmt.Println("Klaras Library health check")
	fmt.Println("---------------------------")
	for _, c := range checks {
		var n int64
		if err := db.Pool.QueryRow(ctx, c.q).Scan(&n); err != nil {
			fmt.Printf("  %-32s ERROR: %v\n", c.name, err)
			continue
		}
		fmt.Printf("  %-32s %d\n", c.name, n)
	}

	// Missing files are checked against the filesystem, which is the only place
	// that truth lives.
	rows, err := db.Pool.Query(ctx, `
		SELECT b.id, b.path, f.filename FROM books b
		JOIN book_files f ON f.book_id = b.id
		ORDER BY b.id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var checked, missing int64
	var examples []string
	for rows.Next() {
		var id int64
		var p, name string
		if err := rows.Scan(&id, &p, &name); err != nil {
			return err
		}
		checked++
		if _, err := os.Stat(filepath.Join(cfg.LibraryRoot, p, name)); err != nil {
			missing++
			if len(examples) < 5 {
				examples = append(examples, fmt.Sprintf("book %d: %s/%s", id, p, name))
			}
		}
	}
	fmt.Printf("  %-32s %d of %d\n", "files missing on disk", missing, checked)
	for _, e := range examples {
		fmt.Printf("      %s\n", e)
	}
	return rows.Err()
}

func cmdDevSeed() error {
	fs := flag.NewFlagSet("dev-seed", flag.ExitOnError)
	books := fs.Int("books", 30000, "number of synthetic books to create")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg)
	ctx := context.Background()

	if err := store.Migrate(ctx, cfg.DatabaseURL, log); err != nil {
		return err
	}
	db, err := store.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns, log)
	if err != nil {
		return err
	}
	defer db.Close()

	// Seeding truncates the library tables. Refuse to do that silently to a
	// database that already holds books.
	var existing int64
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM books").Scan(&existing); err != nil {
		return err
	}
	if existing > 0 && !*yes {
		return fmt.Errorf("database already holds %d books; dev-seed would DELETE them. "+
			"Re-run with --yes if that is really what you want", existing)
	}

	_, err = devseed.Run(ctx, db.Pool, *books, log)
	return err
}

func newLogger(cfg *config.Config) *slog.Logger {
	var level slog.Level
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	if strings.EqualFold(cfg.LogFormat, "json") {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}
