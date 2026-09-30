package management

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nhirsama/yukibot/internal/kernel"
)

// AdminHelp is the /admin help text. It must not start with a slash, or an
// outgoing copy of the reply would be recognized as another command.
const AdminHelp = `管理命令:
/admin admin list - 列出当前账号和委派管理员
/admin admin add <user_id> - 添加委派管理员
/admin admin remove <user_id> - 删除委派管理员
/admin module list - 列出可管理模块及其状态
/admin module enable <name> - 启用模块
/admin module disable <name> - 停用模块`

// CommandName is the registered root.
const CommandName = "/admin"

// CommandSummary is the short /help line for /admin.
const CommandSummary = "管理管理员和功能模块"

// Commands parses /admin arguments.
type Commands struct {
	service *Service
}

var _ kernel.CommandHandler = (*Commands)(nil).Handle

// NewCommands returns the /admin handler.
func NewCommands(service *Service) *Commands {
	return &Commands{service: service}
}

// Handle runs one /admin command. Recognized argument errors are replies.
// Unexpected failures are returned so the dispatcher can log them.
func (c *Commands) Handle(ctx context.Context, command kernel.ControlCommand) (kernel.CommandResult, error) {
	arguments, err := split(command.RawArguments)
	if err != nil {
		return kernel.TextResult("Invalid arguments: " + err.Error()), nil
	}
	if len(arguments) == 0 || (len(arguments) == 1 && arguments[0] == "help") {
		return kernel.TextResult(AdminHelp), nil
	}
	switch {
	case len(arguments) == 2 && arguments[0] == "admin" && arguments[1] == "list":
		return reply(c.listAdmins(ctx))
	case len(arguments) == 3 && arguments[0] == "admin" && arguments[1] == "add":
		return reply(c.addAdmin(ctx, command, arguments[2]))
	case len(arguments) == 3 && arguments[0] == "admin" && arguments[1] == "remove":
		return reply(c.removeAdmin(ctx, command, arguments[2]))
	case len(arguments) == 2 && arguments[0] == "module" && arguments[1] == "list":
		return reply(c.listModules(ctx))
	case len(arguments) == 3 && arguments[0] == "module" && arguments[1] == "enable":
		return reply(c.enableModule(ctx, arguments[2]))
	case len(arguments) == 3 && arguments[0] == "module" && arguments[1] == "disable":
		return reply(c.disableModule(ctx, arguments[2]))
	default:
		return kernel.TextResult(AdminHelp), nil
	}
}

func (c *Commands) listAdmins(ctx context.Context) (string, error) {
	owner, admins, err := c.service.ListAdmins(ctx)
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, 1+len(admins))
	lines = append(lines, fmt.Sprintf("owner: %d", owner))
	for _, userID := range admins {
		lines = append(lines, fmt.Sprintf("admin: %d", userID))
	}
	return strings.Join(lines, "\n"), nil
}

func (c *Commands) addAdmin(ctx context.Context, command kernel.ControlCommand, token string) (string, error) {
	userID, err := parseUserID(token)
	if err != nil {
		return "", err
	}
	if err := c.service.AddAdmin(ctx, command, userID); err != nil {
		return "", err
	}
	return fmt.Sprintf("Administrator %d is enabled.", userID), nil
}

func (c *Commands) removeAdmin(ctx context.Context, command kernel.ControlCommand, token string) (string, error) {
	userID, err := parseUserID(token)
	if err != nil {
		return "", err
	}
	if err := c.service.RemoveAdmin(ctx, command, userID); err != nil {
		return "", err
	}
	return fmt.Sprintf("Administrator %d is removed.", userID), nil
}

func (c *Commands) listModules(ctx context.Context) (string, error) {
	modules, err := c.service.ListModules(ctx)
	if err != nil {
		return "", err
	}
	if len(modules) == 0 {
		return "No manageable modules.", nil
	}
	lines := make([]string, len(modules))
	for i, module := range modules {
		lines[i] = fmt.Sprintf("%s: enabled=%t, running=%t", module.Name, module.Enabled, module.Running)
	}
	return strings.Join(lines, "\n"), nil
}

func (c *Commands) enableModule(ctx context.Context, name string) (string, error) {
	module, err := c.service.EnableModule(ctx, name)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Module %s is enabled and running.", module.Name), nil
}

func (c *Commands) disableModule(ctx context.Context, name string) (string, error) {
	module, err := c.service.DisableModule(ctx, name)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Module %s is disabled.", module.Name), nil
}

func reply(text string, err error) (kernel.CommandResult, error) {
	if err != nil {
		if message, ok := commandMessage(err); ok {
			return kernel.TextResult(message), nil
		}
		return kernel.CommandResult{}, err
	}
	return kernel.TextResult(text), nil
}

func commandMessage(err error) (string, bool) {
	var permission *PermissionError
	if errors.As(err, &permission) {
		return permission.Error(), true
	}
	var value *ValueError
	if errors.As(err, &value) {
		return value.Error(), true
	}
	var missing *kernel.ModuleNotFoundError
	if errors.As(err, &missing) {
		return missing.Error(), true
	}
	return "", false
}
