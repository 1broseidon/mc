package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/1broseidon/mc/internal/browser"
	"github.com/1broseidon/mc/internal/contract"
)

func newBrowseCommand() *cobra.Command {
	var inputFile string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "browse",
		Short: "Run a browser pipeline through Chrome DevTools Protocol",
		Example: `  mycomputer browse --input-file browser.json --json
  cat browser.json | mycomputer browse --input-file - --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if inputFile == "" {
				return contract.Validation("INPUT_FILE_REQUIRED", "browse requires --input-file", nil)
			}
			cfg, err := effectiveConfig()
			if err != nil {
				return err
			}
			var req browser.PipelineRequest
			if err := readJSONFile(inputFile, &req); err != nil {
				return err
			}
			if req.BrowserBin == "" {
				req.BrowserBin = cfg.BrowserBin
			}
			if req.Endpoint == "" {
				req.Endpoint = cfg.BrowserEndpoint
			}
			if req.ScreenshotDir == "" {
				req.ScreenshotDir = cfg.ScreenshotDir
			}
			ctx := cmd.Context()
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			out, err := browser.RunPipeline(ctx, req)
			if err != nil {
				return err
			}
			if rootOpts.JSON {
				return writeJSON(cmd.OutOrStdout(), out)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "url\t%s\n", out.URL)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "title\t%s\n", out.Title)
			if out.Screenshot != "" {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "screenshot\t%s\n", out.Screenshot)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&inputFile, "input-file", "", "read JSON browser pipeline from path; - means stdin")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "cancel the browser pipeline after this duration")
	return cmd
}
