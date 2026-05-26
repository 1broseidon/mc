package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newConfigCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Show effective config and available backends",
		Example: `  mycomputer config
  mycomputer config --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := effectiveConfig()
			if err != nil {
				return err
			}
			report := cfg.Report()
			if rootOpts.JSON {
				return writeJSON(cmd.OutOrStdout(), report)
			}
			rows := [][]string{{"KEY", "VALUE", "SOURCE"}}
			for key, value := range report.Values {
				rows = append(rows, []string{key, fmt.Sprint(value.Value), value.Source})
			}
			printTable(cmd.OutOrStdout(), rows)
			return nil
		},
	}
}
