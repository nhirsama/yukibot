package telegram

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/nhirsama/yukibot/internal/version"
	"go.uber.org/zap"
)

// Client is the gotd user session. Login and the peer manager happen inside Run.
type Client struct {
	apiID       int
	apiHash     string
	sessionPath string
	limiter     *RequestLimiter
	identity    *AccountIdentity

	mu      sync.Mutex
	raw     *telegram.Client
	api     *tg.Client
	manager *peers.Manager
	disp    *tg.UpdateDispatcher

	titles    map[int64]string
	usernames map[int64]string
	forums    map[int64]bool
	joined    map[int64]struct{}

	runCancel context.CancelFunc
	done      chan struct{}
	runErr    error
	started   bool
}

// NewClient builds the update dispatcher before Run so handlers can be registered first.
func NewClient(apiID int, apiHash, sessionPath string, limiter *RequestLimiter, identity *AccountIdentity) *Client {
	if limiter == nil {
		limiter = NewRequestLimiter()
	}
	if identity == nil {
		identity = &AccountIdentity{}
	}
	disp := tg.NewUpdateDispatcher()
	return &Client{
		apiID:       apiID,
		apiHash:     apiHash,
		sessionPath: sessionPath,
		limiter:     limiter,
		identity:    identity,
		disp:        &disp,
		titles:      map[int64]string{},
		usernames:   map[int64]string{},
		forums:      map[int64]bool{},
		joined:      map[int64]struct{}{},
	}
}

// Name is the lifecycle name.
func (c *Client) Name() string { return "telegram-client" }

// Dispatcher is the handler map registered before Run.
func (c *Client) Dispatcher() *tg.UpdateDispatcher { return c.disp }

// Limiter is the process-wide request limiter.
func (c *Client) Limiter() *RequestLimiter { return c.limiter }

// Identity is the mutable account holder.
func (c *Client) Identity() *AccountIdentity { return c.identity }

// API returns the RPC client. It exists only after Start has connected.
func (c *Client) API() (*tg.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.api == nil {
		return nil, errors.New("telegram client is not connected")
	}
	return c.api, nil
}

// Peers returns the peer manager. It exists only inside a running session.
func (c *Client) Peers() (*peers.Manager, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.manager == nil {
		return nil, errors.New("telegram peer manager is not ready")
	}
	return c.manager, nil
}

// Handle implements telegram.UpdateHandler.
// Until the peer manager exists, updates go straight to the dispatcher.
func (c *Client) Handle(ctx context.Context, updates tg.UpdatesClass) error {
	c.mu.Lock()
	manager := c.manager
	disp := c.disp
	c.mu.Unlock()
	if disp == nil {
		return nil
	}
	if manager == nil {
		return disp.Handle(ctx, updates)
	}
	return manager.UpdateHook(disp).Handle(ctx, updates)
}

// Start connects, authorizes, and leaves Run in a supervised goroutine.
func (c *Client) Start(ctx context.Context) error {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(c.sessionPath), 0o755); err != nil {
		return err
	}
	raw := telegram.NewClient(c.apiID, c.apiHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: c.sessionPath},
		UpdateHandler:  c,
		Device: telegram.DeviceConfig{
			DeviceModel: "yukibot",
			AppVersion:  version.Version,
		},
		Logger: zap.NewNop(),
	})
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	ready := make(chan error, 1)
	done := make(chan struct{})
	c.mu.Lock()
	c.raw = raw
	c.runCancel = cancel
	c.done = done
	c.runErr = nil
	c.started = true
	c.mu.Unlock()
	go func() {
		err := raw.Run(runCtx, func(runCtx context.Context) error {
			if err := c.authorize(runCtx, raw); err != nil {
				ready <- err
				return err
			}
			ready <- nil
			<-runCtx.Done()
			return nil
		})
		c.mu.Lock()
		c.runErr = err
		c.mu.Unlock()
		close(done)
	}()
	select {
	case err := <-ready:
		if err != nil {
			c.failStart(cancel, done)
			return err
		}
		return nil
	case <-done:
		c.mu.Lock()
		err := c.runErr
		c.mu.Unlock()
		c.failStart(cancel, done)
		if err == nil {
			err = errors.New("telegram client stopped before authorization")
		}
		return err
	case <-ctx.Done():
		c.failStart(cancel, done)
		return ctx.Err()
	}
}

func (c *Client) authorize(ctx context.Context, raw *telegram.Client) error {
	status, err := raw.Auth().Status(ctx)
	if err != nil {
		return err
	}
	user := status.User
	if !status.Authorized {
		flow := auth.NewFlow(&terminalAuth{in: os.Stdin, out: os.Stderr}, auth.SendCodeOptions{})
		if err := raw.Auth().IfNecessary(ctx, flow); err != nil {
			return err
		}
		user, err = raw.Self(ctx)
		if err != nil {
			return err
		}
	}
	if user == nil || user.ID <= 0 {
		return errors.New("telegram account identity is not available")
	}
	manager := peers.Options{}.Build(raw.API())
	c.mu.Lock()
	c.api = raw.API()
	c.manager = manager
	c.mu.Unlock()
	c.identity.Set(user.ID)
	return c.loadDialogs(ctx)
}

func (c *Client) failStart(cancel context.CancelFunc, done <-chan struct{}) {
	cancel()
	<-done
	c.identity.Clear()
	c.mu.Lock()
	c.raw = nil
	c.api = nil
	c.manager = nil
	c.runCancel = nil
	c.done = nil
	c.started = false
	c.mu.Unlock()
}

// Stop cancels Run and waits for it to finish.
func (c *Client) Stop(ctx context.Context) error {
	c.mu.Lock()
	cancel := c.runCancel
	done := c.done
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	select {
	case <-done:
		c.mu.Lock()
		c.started = false
		c.api = nil
		c.manager = nil
		c.runCancel = nil
		c.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Wait blocks until Run returns. context.Canceled is a clean stop.
func (c *Client) Wait(ctx context.Context) error {
	c.mu.Lock()
	done := c.done
	c.mu.Unlock()
	if done == nil {
		return errors.New("telegram client is not running")
	}
	select {
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil
		}
		return ctx.Err()
	case <-done:
		c.mu.Lock()
		err := c.runErr
		c.mu.Unlock()
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
}
