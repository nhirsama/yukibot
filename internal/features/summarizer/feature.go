package summarizer

// FeatureName is the process feature identifier.
const FeatureName = "summarizer"

// CommandName is the slash command the composition root registers.
const CommandName = "/summary"

// CommandSummary is the short description shown by /help.
const CommandSummary = "生成并发送消息总结"

// CommandSpec is the registration metadata for the kernel command registry.
// This package does not import the kernel; the composition root adapts Handle.
type CommandSpec struct {
	Name     string
	Summary  string
	HelpText string
}

// Spec returns the /summary registration metadata.
func Spec() CommandSpec {
	return CommandSpec{Name: CommandName, Summary: CommandSummary, HelpText: SummaryHelp}
}
