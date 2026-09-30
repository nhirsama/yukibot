//go:build live

package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nhirsama/yukibot/internal/adapters/telegram"
	"github.com/nhirsama/yukibot/internal/config"
)

// TestLiveAccount starts the logged-in session and exercises the control
// commands in Saved Messages. It does not join chats or create routes.
// Same-session sends are not echoed as full updates, so the stored message is
// replayed through the update handler a phone session would hit.
func TestLiveAccount(t *testing.T) {
	chdirModule(t)
	settings, err := config.Load()
	mustLive(t, err)
	app, client, err := assemble(settings)
	mustLive(t, err)

	startCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	mustLive(t, app.Lifecycle.Start(startCtx))
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer stopCancel()
		if err := app.Lifecycle.Stop(stopCtx); err != nil {
			t.Errorf("stop: %s", redactLive(err.Error()))
		}
	})

	driver, err := telegram.NewSavedDriver(client)
	mustLive(t, err)
	cmdCtx, cmdCancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cmdCancel()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_ = liveCommand(t, ctx, driver, "/admin module enable forwarder", false)
		_ = liveCommand(t, ctx, driver, "/admin module enable summarizer", false)
		driver.Delete(ctx)
	})

	mustLive(t, driver.Sweep(cmdCtx, liveCommands()))
	natural := probeNatural(t, cmdCtx, driver)
	t.Logf("saved-message updates from this session: %v", natural)

	checks := []struct{ command, want string }{
		{"/help", "/route - 管理消息转发路由"},
		{"/help", "/summary - 生成并发送消息总结"},
		{"/help", "/admin - 管理管理员和功能模块"},
		{"/help /route", "转发路由命令:"},
		{"/help /admin", "/admin module list"},
		{"/help /summary", "/summary list"},
		{"/help /missing", "未知命令: /missing"},
		{"/admin module list", "forwarder: enabled=true, running=true"},
		{"/admin module list", "summarizer: enabled=true, running=true"},
		{"/admin admin list", "owner: "},
		{"/admin module disable nosuch", `module "nosuch" does not exist`},
		{"/route list", "No forwarding routes."},
		{"/route show 1", "route 1 does not exist"},
		{"/route add not-a-chat -1001", "频道引用必须是 ID、@用户名或 Telegram 邀请链接"},
		{"/route check", "频道检查完成:"},
		{"/route rebuild status", "重建状态: 未运行"},
		{"/summary list", "No summary rules."},
		{"/summary model show", "Summary model is not configured."},
		{"/summary prompt list", "focused:"},
		{"/summary prompt show", "消息总结模型未配置"},
		{"/summary show 1", "summary rule 1 does not exist"},
		{"/summary add not-a-chat me", "聊天引用必须是 ID、@用户名、公开链接或话题链接"},
	}
	for _, check := range checks {
		reply := liveCommand(t, cmdCtx, driver, check.command, natural)
		if !strings.Contains(reply, check.want) {
			t.Fatalf("%s\nreply: %s\nwant substring: %s", check.command, redactLive(reply), check.want)
		}
	}

	reply := liveCommand(t, cmdCtx, driver, "live-probe-not-a-command", natural)
	if reply != "" {
		t.Fatalf("plain text was treated as a command: %s", redactLive(reply))
	}

	for _, name := range []string{"summarizer", "forwarder"} {
		disabled := liveCommand(t, cmdCtx, driver, "/admin module disable "+name, natural)
		if !strings.Contains(disabled, "Module "+name+" is disabled.") {
			t.Fatalf("disable %s: %s", name, redactLive(disabled))
		}
		listed := liveCommand(t, cmdCtx, driver, "/admin module list", natural)
		if !strings.Contains(listed, name+": enabled=false, running=false") {
			t.Fatalf("after disable %s: %s", name, redactLive(listed))
		}
		enabled := liveCommand(t, cmdCtx, driver, "/admin module enable "+name, natural)
		if !strings.Contains(enabled, "Module "+name+" is enabled and running.") {
			t.Fatalf("enable %s: %s", name, redactLive(enabled))
		}
	}
}

func liveCommands() []string {
	return []string{
		"/help",
		"/help /route",
		"/help /admin",
		"/help /summary",
		"/help /missing",
		"/admin module list",
		"/admin admin list",
		"/admin module disable nosuch",
		"/admin module disable summarizer",
		"/admin module enable summarizer",
		"/admin module disable forwarder",
		"/admin module enable forwarder",
		"/route list",
		"/route show 1",
		"/route add not-a-chat -1001",
		"/route check",
		"/route rebuild status",
		"/summary list",
		"/summary model show",
		"/summary prompt list",
		"/summary prompt show",
		"/summary show 1",
		"/summary add not-a-chat me",
		"live-probe-not-a-command",
	}
}

func probeNatural(t *testing.T, ctx context.Context, driver *telegram.SavedDriver) bool {
	t.Helper()
	id, err := driver.Send(ctx, "/help")
	mustLive(t, err)
	reply, ok, err := driver.WaitReply(ctx, id, 5*time.Second)
	mustLive(t, err)
	if ok {
		if !strings.Contains(reply, "/route - 管理消息转发路由") {
			t.Fatalf("initial /help failed: %s", redactLive(reply))
		}
		return true
	}
	mustLive(t, driver.Replay(ctx, id))
	reply, ok, err = driver.WaitReply(ctx, id, 8*time.Second)
	mustLive(t, err)
	if !ok || !strings.Contains(reply, "/route - 管理消息转发路由") {
		t.Fatalf("initial /help failed: %s", redactLive(reply))
	}
	return false
}

func liveCommand(t *testing.T, ctx context.Context, driver *telegram.SavedDriver, text string, natural bool) string {
	t.Helper()
	id, err := driver.Send(ctx, text)
	mustLive(t, err)
	plain := text == "" || text[0] != '/'
	if !natural {
		mustLive(t, driver.Replay(ctx, id))
	}
	timeout := 8 * time.Second
	if plain {
		timeout = time.Second
	}
	reply, ok, err := driver.WaitReply(ctx, id, timeout)
	mustLive(t, err)
	if !ok {
		if plain {
			return ""
		}
		t.Fatalf("%s produced no reply", text)
	}
	return reply
}

func chdirModule(t *testing.T) {
	t.Helper()
	dir, err := os.Getwd()
	mustLive(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			mustLive(t, os.Chdir(dir))
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func mustLive(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(redactLive(err.Error()))
	}
}

func redactLive(msg string) string {
	lines := strings.Split(msg, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "owner: ") || strings.HasPrefix(line, "admin: ") {
			label, _, _ := strings.Cut(line, ": ")
			lines[i] = label + ": [id]"
		}
	}
	msg = strings.Join(lines, "\n")
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
