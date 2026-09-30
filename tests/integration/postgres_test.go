package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nhirsama/yukibot/internal/adapters/database"
	"github.com/nhirsama/yukibot/internal/contracts"
	"github.com/nhirsama/yukibot/internal/features/forwarder"
	fwdstore "github.com/nhirsama/yukibot/internal/features/forwarder/store"
	"github.com/nhirsama/yukibot/internal/features/management"
	mgmtstore "github.com/nhirsama/yukibot/internal/features/management/store"
	"github.com/nhirsama/yukibot/internal/features/summarizer"
	sumstore "github.com/nhirsama/yukibot/internal/features/summarizer/store"
)

// TestPostgreSQL applies the real schema and checks migration checksums,
// drift, statement rollback, head-of-line claim_due, and repository round-trips.
// It is skipped unless YUKIBOT_DATABASE_URL is set, so default go test stays offline.
func TestPostgreSQL(t *testing.T) {
	url := os.Getenv("YUKIBOT_DATABASE_URL")
	if url == "" {
		t.Skip("set YUKIBOT_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := database.Open(ctx, url)
	must(t, err)
	t.Cleanup(db.Close)
	dropProbes(db)
	t.Cleanup(func() { dropProbes(db) })

	t.Run("upgrade", func(t *testing.T) { testUpgrade(t, ctx, db) })
	t.Run("drift", func(t *testing.T) { testDrift(t, ctx, db) })
	t.Run("rollback", func(t *testing.T) { testRollback(t, ctx, db) })
	t.Run("jobs", func(t *testing.T) { testJobs(t, ctx, db) })
	t.Run("repositories", func(t *testing.T) { testRepositories(t, ctx, db) })
}

func testUpgrade(t *testing.T, ctx context.Context, db *database.DB) {
	t.Helper()
	migrations := productMigrations()
	runner, err := database.NewRunner(db, migrations)
	must(t, err)
	if _, err := runner.Upgrade(ctx); err != nil {
		must(t, err)
	}
	again, err := runner.Upgrade(ctx)
	must(t, err)
	if len(again) != 0 {
		fail(t, "second upgrade applied %v", again)
	}
	applied := loadApplied(t, ctx, db)
	if len(applied) != len(migrations) {
		fail(t, "applied migrations = %d, want %d (%v)", len(applied), len(migrations), applied)
	}
	for _, migration := range migrations {
		key := fmt.Sprintf("%s:%d", migration.Scope, migration.Version)
		got, ok := applied[key]
		if !ok {
			fail(t, "missing migration %s", key)
		}
		if got != database.Checksum(migration.Statements) {
			fail(t, "checksum drift on %s", key)
		}
	}
}

func testDrift(t *testing.T, ctx context.Context, db *database.DB) {
	t.Helper()
	original := contracts.Migration{
		Scope:       "itest",
		Version:     1,
		Description: "probe",
		Statements:  []string{`CREATE TABLE itest_probe (id integer PRIMARY KEY)`},
	}
	next := contracts.Migration{
		Scope:       "itest",
		Version:     2,
		Description: "must not apply after drift",
		Statements:  []string{`CREATE TABLE itest_next (id integer PRIMARY KEY)`},
	}
	runner, err := database.NewRunner(db, []contracts.Migration{original})
	must(t, err)
	if _, err := runner.Upgrade(ctx); err != nil {
		must(t, err)
	}
	changed := original
	changed.Statements = []string{`CREATE TABLE itest_probe (id integer PRIMARY KEY, extra integer)`}
	drifted, err := database.NewRunner(db, []contracts.Migration{changed, next})
	must(t, err)
	_, err = drifted.Upgrade(ctx)
	var drift *contracts.MigrationDriftError
	if !errors.As(err, &drift) || drift.Scope != "itest" || drift.Version != 1 {
		fail(t, "drift error = %v", err)
	}
	if tableExists(t, ctx, db, "itest_next") {
		fail(t, "checksum drift applied a later migration")
	}
	stored := loadApplied(t, ctx, db)["itest:1"]
	if stored != database.Checksum(original.Statements) {
		fail(t, "drift rewrote the stored checksum")
	}
}

func testRollback(t *testing.T, ctx context.Context, db *database.DB) {
	t.Helper()
	broken := contracts.Migration{
		Scope:       "itestfail",
		Version:     1,
		Description: "statement failure rolls the migration back",
		Statements: []string{
			`CREATE TABLE itest_fail (id integer PRIMARY KEY)`,
			`INSERT INTO itest_fail (id) VALUES (1), (1)`,
		},
	}
	runner, err := database.NewRunner(db, []contracts.Migration{broken})
	must(t, err)
	if _, err := runner.Upgrade(ctx); err == nil {
		fail(t, "broken migration succeeded")
	}
	if tableExists(t, ctx, db, "itest_fail") {
		fail(t, "failed migration left its table behind")
	}
	if _, ok := loadApplied(t, ctx, db)["itestfail:1"]; ok {
		fail(t, "failed migration was recorded")
	}
}

func testJobs(t *testing.T, ctx context.Context, db *database.DB) {
	t.Helper()
	must(t, exec(ctx, db, `DELETE FROM forwarder_jobs`))
	t.Cleanup(func() { _ = exec(context.Background(), db, `DELETE FROM forwarder_jobs`) })
	repo := fwdstore.NewRepository(db)
	now := time.Now().UTC().Truncate(time.Microsecond)

	inserted, err := repo.Enqueue(ctx, []forwarder.PendingForwardJob{
		{Kind: forwarder.ForwardJobReceive, DeduplicationKey: "receive:-1001:10", AvailableAt: now.Add(10 * time.Second), Event: received(t, -1001, 10, now)},
		{Kind: forwarder.ForwardJobReceive, DeduplicationKey: "receive:-1001:11", AvailableAt: now, Event: received(t, -1001, 11, now)},
	})
	must(t, err)
	if inserted != 2 {
		fail(t, "inserted %d, want 2", inserted)
	}
	blocked, err := repo.ClaimDue(ctx, now)
	must(t, err)
	if len(blocked) != 0 {
		fail(t, "future head must block a later due job, got %d", len(blocked))
	}
	must(t, exec(ctx, db, `DELETE FROM forwarder_jobs`))

	group := "album:-1001:50"
	if _, err := repo.Enqueue(ctx, []forwarder.PendingForwardJob{
		{Kind: forwarder.ForwardJobReceive, DeduplicationKey: "receive:-1001:10", AvailableAt: now.Add(time.Second), GroupKey: &group, Event: received(t, -1001, 10, now)},
		{Kind: forwarder.ForwardJobReceive, DeduplicationKey: "receive:-1001:11", AvailableAt: now.Add(5 * time.Second), GroupKey: &group, Event: received(t, -1001, 11, now)},
	}); err != nil {
		must(t, err)
	}
	slid, err := repo.Enqueue(ctx, []forwarder.PendingForwardJob{
		{Kind: forwarder.ForwardJobReceive, DeduplicationKey: "receive:-1001:10", AvailableAt: now.Add(8 * time.Second), GroupKey: &group, Event: received(t, -1001, 10, now)},
	})
	must(t, err)
	if slid != 0 {
		fail(t, "duplicate insert = %d, want 0", slid)
	}
	if jobs, err := repo.ClaimDue(ctx, now.Add(5*time.Second)); err != nil || len(jobs) != 0 {
		must(t, err)
		fail(t, "duplicate key must slide the group, got %d jobs", len(jobs))
	}
	claimed, err := repo.ClaimDue(ctx, now.Add(8*time.Second))
	must(t, err)
	if len(claimed) != 2 || claimed[0].Attempts != 1 || claimed[1].Attempts != 1 {
		fail(t, "group claim = %+v", claimed)
	}
	recovered, err := repo.RecoverIncomplete(ctx)
	must(t, err)
	if recovered != 2 {
		fail(t, "recovered %d, want 2", recovered)
	}
	var attempts int
	var state string
	if err := db.QueryRow(ctx, `SELECT attempts, state FROM forwarder_jobs WHERE deduplication_key = $1`, "receive:-1001:10").Scan(&attempts, &state); err != nil {
		must(t, err)
	}
	if attempts != 1 || state != "pending" {
		fail(t, "recover reset the job to attempts=%d state=%s", attempts, state)
	}
	again, err := repo.ClaimDue(ctx, now.Add(8*time.Second))
	must(t, err)
	if len(again) != 2 || again[0].Attempts != 2 || again[1].Attempts != 2 {
		fail(t, "second claim = %+v", again)
	}
	ids := []int{again[0].ID, again[1].ID}
	must(t, repo.MarkSucceeded(ctx, ids))
	var left int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM forwarder_jobs`).Scan(&left); err != nil {
		must(t, err)
	}
	if left != 0 {
		fail(t, "success left %d jobs", left)
	}
	if jobs, err := repo.ClaimDue(ctx, now.Add(8*time.Second)); err != nil || len(jobs) != 0 {
		must(t, err)
		fail(t, "claim after delete = %d", len(jobs))
	}
	reinserted, err := repo.Enqueue(ctx, []forwarder.PendingForwardJob{
		{Kind: forwarder.ForwardJobReceive, DeduplicationKey: "receive:-1001:10", AvailableAt: now, GroupKey: &group, Event: received(t, -1001, 10, now)},
	})
	must(t, err)
	if reinserted != 1 {
		fail(t, "deleted key inserted %d, want 1", reinserted)
	}
}

func testRepositories(t *testing.T, ctx context.Context, db *database.DB) {
	t.Helper()
	const (
		sourceChat = int64(-100910000001)
		destChat   = int64(-100910000002)
		adminID    = int64(910000003)
		accountID  = int64(910000004)
		ruleSource = int64(-100910000011)
		ruleDest   = int64(-100910000012)
	)
	t.Cleanup(func() {
		c := context.Background()
		_ = exec(c, db, `DELETE FROM forwarder_routes WHERE source_chat_id = $1`, sourceChat)
		_ = exec(c, db, `DELETE FROM summarizer_rules WHERE source_chat_id = $1`, ruleSource)
		_ = exec(c, db, `DELETE FROM summarizer_model_config WHERE id = 1`)
		_ = exec(c, db, `DELETE FROM management_admins WHERE user_id = $1`, adminID)
		_ = exec(c, db, `DELETE FROM management_command_receipts WHERE account_id = $1`, accountID)
		_ = exec(c, db, `DELETE FROM management_modules WHERE name = 'itest'`)
	})

	routes := fwdstore.NewRepository(db)
	source, err := forwarder.NewSourceEndpoint(sourceChat, forwarder.SourceConfig{})
	must(t, err)
	destination, err := forwarder.NewDestinationEndpoint(destChat, forwarder.DestinationConfig{})
	must(t, err)
	route, err := routes.AddAuto(ctx, forwarder.NewRouteDraft(source, destination))
	must(t, err)
	listed, err := routes.ListForSourceChat(ctx, sourceChat)
	must(t, err)
	if len(listed) != 1 || listed[0].ID != route.ID || listed[0].Mode != forwarder.ForwardModeForward || !listed[0].FallbackToCopy {
		fail(t, "route round-trip = %+v", listed)
	}
	removed, err := routes.Remove(ctx, route.ID)
	must(t, err)
	if !removed {
		fail(t, "route remove reported false")
	}

	rules := sumstore.NewRepository(db)
	from, err := summarizer.NewSummaryEndpoint(ruleSource, nil, nil)
	must(t, err)
	to, err := summarizer.NewSummaryEndpoint(ruleDest, nil, nil)
	must(t, err)
	missing, err := summarizer.NewSummaryRule(999999, from, to, 60, true)
	must(t, err)
	if err := rules.Replace(ctx, missing); !errors.Is(err, summarizer.ErrRuleMissing) {
		fail(t, "replace missing = %v", err)
	}
	draft, err := summarizer.NewSummaryRuleDraft(from, to, 120, true)
	must(t, err)
	rule, err := rules.AddAuto(ctx, draft)
	must(t, err)
	found, err := rules.ListAll(ctx)
	must(t, err)
	seen := false
	for _, item := range found {
		if item.ID == rule.ID && item.WindowSeconds == 120 && item.Enabled {
			seen = true
		}
	}
	if !seen {
		fail(t, "summary rule %d was not listed", rule.ID)
	}
	ok, err := rules.Remove(ctx, rule.ID)
	must(t, err)
	if !ok {
		fail(t, "summary rule remove reported false")
	}

	cfg, err := summarizer.NewSummaryModelConfig("openai", "gpt-test", nil, nil)
	must(t, err)
	must(t, rules.SaveModelConfig(ctx, cfg))
	var apiKey, baseURL, custom *string
	if err := db.QueryRow(ctx, `SELECT api_key, base_url, custom_prompt FROM summarizer_model_config WHERE id = 1`).Scan(&apiKey, &baseURL, &custom); err != nil {
		must(t, err)
	}
	if apiKey != nil || baseURL != nil || custom != nil {
		fail(t, "empty model strings were stored as non-NULL")
	}
	loaded, err := rules.GetModelConfig(ctx)
	must(t, err)
	if loaded == nil || loaded.APIKey != "" || loaded.BaseURL != "" || loaded.CustomPrompt != "" || loaded.Model != "gpt-test" {
		fail(t, "model config = %+v", loaded)
	}
	cleared, err := rules.ClearModelConfig(ctx)
	must(t, err)
	if !cleared {
		fail(t, "clear model config reported false")
	}

	admins := mgmtstore.NewRepository(db, management.Owner{ID: accountID})
	must(t, admins.AddAdmin(ctx, adminID, accountID))
	isAdmin, err := admins.IsAdmin(ctx, adminID)
	must(t, err)
	if !isAdmin {
		fail(t, "admin was not stored")
	}
	must(t, admins.SetEnabled(ctx, "itest", true))
	enabled, err := admins.GetEnabled(ctx, "itest")
	must(t, err)
	if enabled == nil || !*enabled {
		fail(t, "module flag = %v", enabled)
	}
	missingModule, err := admins.GetEnabled(ctx, "itest-missing")
	must(t, err)
	if missingModule != nil {
		fail(t, "missing module = %v", *missingModule)
	}
	if err := admins.MarkProcessed(ctx, sourceChat, 7); err != nil {
		must(t, err)
	}
	processed, err := admins.IsProcessed(ctx, sourceChat, 7)
	must(t, err)
	if !processed {
		fail(t, "receipt was not stored")
	}
	unavailable := mgmtstore.NewRepository(db, management.Owner{})
	_, err = unavailable.IsProcessed(ctx, sourceChat, 7)
	if err == nil || err.Error() != "Telegram account identity is not available" {
		fail(t, "missing identity = %v", err)
	}
}

func productMigrations() []contracts.Migration {
	out := make([]contracts.Migration, 0, len(forwarder.Migrations)+len(management.Migrations)+len(summarizer.SummarizerMigrations))
	out = append(out, forwarder.Migrations...)
	out = append(out, management.Migrations...)
	out = append(out, summarizer.SummarizerMigrations...)
	return out
}

func loadApplied(t *testing.T, ctx context.Context, db *database.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(ctx, `SELECT scope, version, checksum FROM yukibot_schema_migrations`)
	must(t, err)
	defer rows.Close()
	applied := map[string]string{}
	for rows.Next() {
		var scope, checksum string
		var version int
		must(t, rows.Scan(&scope, &version, &checksum))
		applied[fmt.Sprintf("%s:%d", scope, version)] = checksum
	}
	must(t, rows.Err())
	return applied
}

func tableExists(t *testing.T, ctx context.Context, db *database.DB, name string) bool {
	t.Helper()
	var exists bool
	must(t, db.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+name).Scan(&exists))
	return exists
}

func dropProbes(db *database.DB) {
	ctx := context.Background()
	_ = exec(ctx, db, `DROP TABLE IF EXISTS itest_probe, itest_next, itest_fail`)
	_ = exec(ctx, db, `DELETE FROM yukibot_schema_migrations WHERE scope IN ('itest', 'itestfail')`)
}

func exec(ctx context.Context, db *database.DB, sql string, args ...any) error {
	_, err := db.Exec(ctx, sql, args...)
	return err
}

func received(t *testing.T, chat int64, id int, when time.Time) contracts.TelegramMessageReceived {
	t.Helper()
	ref, err := contracts.NewMessageRef(chat, id)
	must(t, err)
	return contracts.TelegramMessageReceived{Message: contracts.TelegramMessage{
		Ref:         ref,
		ContentType: contracts.ContentText,
		OccurredAt:  when,
		Text:        "m",
	}}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(redact(err.Error()))
	}
}

func fail(t *testing.T, format string, args ...any) {
	t.Helper()
	t.Fatal(redact(fmt.Sprintf(format, args...)))
}

func redact(msg string) string {
	var out strings.Builder
	for i := 0; i < len(msg); {
		if strings.HasPrefix(strings.ToLower(msg[i:]), "password=") {
			out.WriteString("password=[redacted]")
			i += len("password=")
			for i < len(msg) && msg[i] != ' ' && msg[i] != '\'' && msg[i] != '"' && msg[i] != '&' {
				i++
			}
			continue
		}
		if i+3 <= len(msg) && msg[i:i+3] == "://" {
			out.WriteString("://")
			i += 3
			at := strings.IndexByte(msg[i:], '@')
			if at > 0 {
				out.WriteString("[redacted]")
				i += at
			}
			continue
		}
		out.WriteByte(msg[i])
		i++
	}
	return out.String()
}
