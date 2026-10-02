package kernel

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"unicode"
)

// ControlCommand is one recognized slash command.
type ControlCommand struct {
	Name         string
	RawArguments string
	ChatID       int64
	MessageID    int
	ActorID      *int64
	Outgoing     bool
}

// CommandResult is the optional reply text from a handler.
type CommandResult struct {
	Text *string
}

// TextResult returns a result with a reply.
func TextResult(text string) CommandResult {
	return CommandResult{Text: &text}
}

// CommandDispatch is the control-plane outcome for one message.
type CommandDispatch struct {
	Consumed  bool
	Response  *string
	Duplicate bool
}

// CommandHandler executes one registered root.
type CommandHandler func(context.Context, ControlCommand) (CommandResult, error)

// CommandRegistration is a module-owned command root.
type CommandRegistration struct {
	Name     string
	Summary  string
	HelpText string
	Handler  CommandHandler
}

// CommandAuthorizer decides whether the actor may run the command.
type CommandAuthorizer interface {
	IsAuthorized(ctx context.Context, command ControlCommand) (bool, error)
}

// CommandReceiptStore deduplicates command execution by chat and message id.
type CommandReceiptStore interface {
	IsProcessed(ctx context.Context, chatID int64, messageID int) (bool, error)
	MarkProcessed(ctx context.Context, chatID int64, messageID int) error
}

// CommandSubscription unregisters one root. Unregister is idempotent.
type CommandSubscription struct {
	active bool
	unreg  func()
}

// Active reports whether the command is still registered.
func (s *CommandSubscription) Active() bool { return s != nil && s.active }

// Unregister removes the command. A second call does nothing.
func (s *CommandSubscription) Unregister() {
	if s == nil || !s.active {
		return
	}
	s.active = false
	if s.unreg != nil {
		s.unreg()
	}
}

// CommandRegistry stores exact command roots.
type CommandRegistry struct {
	mu       sync.Mutex
	commands map[string]CommandRegistration
}

// NewCommandRegistry returns an empty registry.
func NewCommandRegistry() *CommandRegistry {
	return &CommandRegistry{commands: map[string]CommandRegistration{}}
}

// Register adds one slash-prefixed token. /help is reserved.
func (r *CommandRegistry) Register(name, summary, helpText string, handler CommandHandler) (*CommandSubscription, error) {
	if !commandToken(name) {
		return nil, errors.New("a command name must be one slash-prefixed token")
	}
	if name == "/help" {
		return nil, errors.New("/help is reserved by the command framework")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.commands[name]; ok {
		return nil, fmt.Errorf("command %q is already registered", name)
	}
	r.commands[name] = CommandRegistration{Name: name, Summary: summary, HelpText: helpText, Handler: handler}
	sub := &CommandSubscription{active: true}
	sub.unreg = func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(r.commands, name)
	}
	return sub, nil
}

// Get returns the registration or false.
func (r *CommandRegistry) Get(name string) (CommandRegistration, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	reg, ok := r.commands[name]
	return reg, ok
}

// ListCommands returns registrations sorted by name.
func (r *CommandRegistry) ListCommands() []CommandRegistration {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.commands))
	for name := range r.commands {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]CommandRegistration, 0, len(names))
	for _, name := range names {
		out = append(out, r.commands[name])
	}
	return out
}

// Recognizes reports whether text is /help or a registered root.
func (r *CommandRegistry) Recognizes(text string) bool {
	name, _, ok := SplitCommand(text)
	if !ok {
		return false
	}
	if name == "/help" {
		return true
	}
	_, exists := r.Get(name)
	return exists
}

func commandToken(name string) bool {
	if name == "" || name[0] != '/' {
		return false
	}
	for _, r := range name {
		if commandSpace(r) {
			return false
		}
	}
	return true
}

// SplitCommand splits the first token and preserves the remainder, including leading spaces.
func SplitCommand(text string) (string, string, bool) {
	if text == "" || text[0] != '/' {
		return "", "", false
	}
	for index, character := range text {
		if commandSpace(character) {
			return text[:index], text[index+len(string(character)):], true
		}
	}
	return text, "", true
}

// CommandDispatcher authorizes, deduplicates and runs commands serially.
type CommandDispatcher struct {
	registry   *CommandRegistry
	authorizer CommandAuthorizer
	receipts   CommandReceiptStore
	log        Logger
	mu         sync.Mutex
}

// NewCommandDispatcher returns a serial dispatcher.
func NewCommandDispatcher(registry *CommandRegistry, authorizer CommandAuthorizer, receipts CommandReceiptStore, logger Logger) *CommandDispatcher {
	return &CommandDispatcher{registry: registry, authorizer: authorizer, receipts: receipts, log: orLogger(logger)}
}

// Recognizes delegates without taking the execution lock.
func (d *CommandDispatcher) Recognizes(text string) bool { return d.registry.Recognizes(text) }

// Dispatch consumes registered commands and /help.
func (d *CommandDispatcher) Dispatch(ctx context.Context, text string, chatID int64, messageID int, actorID *int64, outgoing bool) (CommandDispatch, error) {
	name, raw, ok := SplitCommand(text)
	if !ok {
		return CommandDispatch{}, nil
	}
	command := ControlCommand{
		Name: name, RawArguments: raw, ChatID: chatID, MessageID: messageID, ActorID: actorID, Outgoing: outgoing,
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	registration, exists := d.registry.Get(name)
	if name != "/help" && !exists {
		return CommandDispatch{}, nil
	}
	processed, err := d.receipts.IsProcessed(ctx, chatID, messageID)
	if err != nil {
		return CommandDispatch{}, err
	}
	if processed {
		return CommandDispatch{Consumed: true, Duplicate: true}, nil
	}
	var response *string
	authorized, err := d.authorizer.IsAuthorized(ctx, command)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return CommandDispatch{}, err
		}
		d.log.Error("control command failed", "command", name, "chat_id", chatID, "message_id", messageID, "actor_id", actorID, "error_type", typeName(err))
		text := "Command failed. Check the application logs."
		response = &text
	} else if !authorized {
		text := "Permission denied."
		response = &text
	} else if name == "/help" {
		text := d.help(raw)
		response = &text
	} else {
		result, herr := registration.Handler(ctx, command)
		if herr != nil {
			if errors.Is(herr, context.Canceled) {
				return CommandDispatch{}, herr
			}
			d.log.Error("control command failed", "command", name, "chat_id", chatID, "message_id", messageID, "actor_id", actorID, "error_type", typeName(herr))
			text := "Command failed. Check the application logs."
			response = &text
		} else {
			response = result.Text
		}
	}
	if err := d.receipts.MarkProcessed(ctx, chatID, messageID); err != nil {
		return CommandDispatch{}, err
	}
	return CommandDispatch{Consumed: true, Response: response}, nil
}

func (d *CommandDispatcher) help(raw string) string {
	requested := trimSpace(raw)
	if requested != "" {
		registration, ok := d.registry.Get(requested)
		if !ok {
			return "未知命令: " + requested
		}
		return registration.HelpText
	}
	lines := []string{"可用命令:", "/help - 列出命令或查看详细帮助"}
	for _, registration := range d.registry.ListCommands() {
		lines = append(lines, registration.Name+" - "+registration.Summary)
	}
	lines = append(lines, "使用 /help /命令 查看详细帮助。")
	return joinLines(lines)
}

func trimSpace(value string) string {
	start, end := 0, len(value)
	for start < end {
		r, size := runeAt(value[start:])
		if !commandSpace(r) {
			break
		}
		start += size
	}
	for end > start {
		r, size := lastRune(value[:end])
		if !commandSpace(r) {
			break
		}
		end -= size
	}
	return value[start:end]
}

// Python str.isspace also recognizes these four ASCII information separators.
// Keep registration, dispatch splitting and help lookup on the same definition.
func commandSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= '\x1c' && r <= '\x1f')
}

func runeAt(s string) (rune, int) {
	for _, r := range s {
		return r, len(string(r))
	}
	return 0, 0
}

func lastRune(s string) (rune, int) {
	var r rune
	var size int
	for _, rr := range s {
		r = rr
		size = len(string(rr))
	}
	return r, size
}

func joinLines(lines []string) string {
	out := ""
	for i, line := range lines {
		if i > 0 {
			out += "\n"
		}
		out += line
	}
	return out
}
