package main

import (
	"github.com/1broseidon/mc/internal/contract"

	"github.com/spf13/cobra"
)

func newCompletionCommand(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:   "completion [bash|zsh|fish|powershell]",
		Short: "Generate shell completion",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return contract.Validation("SHELL_REQUIRED", "completion requires one shell: bash, zsh, fish, or powershell", nil)
			}
			switch args[0] {
			case "bash", "zsh", "fish", "powershell":
				return nil
			default:
				return contract.Validation("SHELL_UNSUPPORTED", "completion shell must be bash, zsh, fish, or powershell", map[string]any{"shell": args[0]})
			}
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return root.GenBashCompletion(cmd.OutOrStdout())
			case "zsh":
				return root.GenZshCompletion(cmd.OutOrStdout())
			case "fish":
				return root.GenFishCompletion(cmd.OutOrStdout(), true)
			default:
				return root.GenPowerShellCompletion(cmd.OutOrStdout())
			}
		},
	}
}
