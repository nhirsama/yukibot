package telegram

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

// terminalAuth asks on stdin. Sign-up is unsupported.
type terminalAuth struct {
	in  io.Reader
	out io.Writer
	mu  sync.Mutex
	buf *bufio.Reader
}

func (t *terminalAuth) Phone(ctx context.Context) (string, error) {
	return t.prompt(ctx, "Phone: ")
}

func (t *terminalAuth) Password(ctx context.Context) (string, error) {
	return t.prompt(ctx, "Password: ")
}

func (t *terminalAuth) Code(ctx context.Context, _ *tg.AuthSentCode) (string, error) {
	return t.prompt(ctx, "Code: ")
}

func (t *terminalAuth) AcceptTermsOfService(context.Context, tg.HelpTermsOfService) error {
	return errors.New("sign up is not supported")
}

func (t *terminalAuth) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("sign up is not supported")
}

func (t *terminalAuth) prompt(ctx context.Context, label string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.buf == nil {
		t.buf = bufio.NewReader(t.in)
	}
	if _, err := fmt.Fprint(t.out, label); err != nil {
		return "", err
	}
	line, err := t.buf.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" && err != nil {
		return "", err
	}
	return line, nil
}
