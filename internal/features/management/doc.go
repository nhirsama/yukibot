// Package management owns bot-wide administrator commands, module toggles,
// and the control-plane authorization and receipt store.
//
// Module lifecycle stays behind the local Modules interface so this package
// does not depend on the concrete kernel controller. Command authorization
// and /admin handling use kernel command types so Service is a
// kernel.CommandAuthorizer and Commands.Handle is a kernel.CommandHandler.
package management
