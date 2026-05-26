package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/input"
)

// newTypeTextCommand exposes the upgraded TypeText routing via the CLI.
// The --via flag selects the routing mode:
//
//	auto  — len>64 OR non-ASCII OR control char OR IME active → paste; else xtest (default)
//	xtest — layout-aware XTest; rejects INPUT_LAYOUT_UNREACHABLE or INPUT_IME_ACTIVE
//	paste — save clipboard, write text, ctrl+v, restore (best-effort)
func newTypeTextCommand() *cobra.Command {
	var via string
	cmd := &cobra.Command{
		Use:   "type-text [text]",
		Short: "Type literal text into the focused target (auto / xtest / paste)",
		Args:  cobra.ExactArgs(1),
		Example: `  mycomputer type-text "hello world" --json
  mycomputer type-text "$(< /etc/os-release)" --via paste --json
  mycomputer type-text "ASCII short" --via xtest --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := input.TypeTextWith(cmd.Context(), input.TypeTextRequest{Text: args[0], Via: via})
			if err != nil {
				return err
			}
			details := map[string]any{"via": res.Via}
			if res.Via == input.TypeTextViaPaste {
				details["clipboard_restored"] = res.ClipboardRestored
			}
			if res.IMEActive {
				details["ime_active"] = true
				if res.IMEEngine != "" {
					details["ime_engine"] = res.IMEEngine
				}
			}
			out := contract.ActionResult{Action: "type_text", OK: true, Backend: backendForVia(res.Via), Details: details}
			if rootOpts.JSON {
				return writeJSON(cmd.OutOrStdout(), out)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "type_text\ttrue\t%s\n", res.Via)
			return nil
		},
	}
	cmd.Flags().StringVar(&via, "via", "auto", "routing: xtest, paste, or auto (default auto)")
	return cmd
}

func backendForVia(via string) string {
	if via == input.TypeTextViaPaste {
		return "x11.clipboard"
	}
	return "XTest"
}
