package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/pipeline"
	"github.com/1broseidon/mc/internal/wait"
)

func newWaitForWindowCommand() *cobra.Command {
	var (
		target    contract.WindowTarget
		present   = true
		focused   bool
		timeoutMS int
		pollMS    int
	)
	cmd := &cobra.Command{
		Use:   "wait-for-window",
		Short: "Wait until a window matching a selector appears or (with --present=false) disappears",
		Example: `  mycomputer wait-for-window --class fam-ui --timeout-ms 5000 --json
  mycomputer wait-for-window --title "Save As" --present=false --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			req := wait.WindowRequest{
				Match:     target,
				TimeoutMS: timeoutMS,
				PollMS:    pollMS,
			}
			req.Present = &present
			if cmd.Flags().Changed("focused") {
				v := focused
				req.Focused = &v
			}
			res, err := pipeline.RunWaitForWindow(cmd.Context(), req)
			if err != nil {
				return err
			}
			if rootOpts.JSON {
				return writeJSON(cmd.OutOrStdout(), res)
			}
			printWindowWait(cmd, res)
			return nil
		},
	}
	cmd.Flags().StringVar(&target.ID, "id", "", "target window id")
	cmd.Flags().StringVar(&target.Title, "title", "", "target substring in window title")
	cmd.Flags().StringVar(&target.Class, "class", "", "target WM_CLASS")
	cmd.Flags().Uint32Var(&target.PID, "pid", 0, "target process id")
	cmd.Flags().BoolVar(&present, "present", true, "wait for the window to be present (default) or absent (--present=false)")
	cmd.Flags().BoolVar(&focused, "focused", false, "require the matched window to be focused (only honored when set explicitly)")
	cmd.Flags().IntVar(&timeoutMS, "timeout-ms", wait.DefaultWindowTimeoutMS, "wait timeout in milliseconds")
	cmd.Flags().IntVar(&pollMS, "poll-ms", wait.DefaultWindowPollMS, "poll cadence in milliseconds")
	return cmd
}

func newWaitForPixelChangeCommand() *cobra.Command {
	var (
		region    string
		threshold float64
		timeoutMS int
		pollMS    int
		mode      string
		stableMS  int
	)
	cmd := &cobra.Command{
		Use:   "wait-for-pixel-change",
		Short: "Wait until pixels in a region change (mode any) or stop changing (mode stable)",
		Example: `  mycomputer wait-for-pixel-change --region 100,100,400,200 --threshold 0.05 --json
  mycomputer wait-for-pixel-change --region 0,0,800,600 --mode stable --stable-ms 500 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if region == "" {
				return contract.Validation("WAIT_REGION_REQUIRED", "wait-for-pixel-change requires --region", nil)
			}
			b, err := parseBounds(region)
			if err != nil {
				return err
			}
			res, err := pipeline.RunWaitForPixelChange(cmd.Context(), wait.PixelRequest{
				Region:    contract.RegionRefFromBounds(b),
				Threshold: threshold,
				TimeoutMS: timeoutMS,
				PollMS:    pollMS,
				Mode:      mode,
				StableMS:  stableMS,
			})
			if err != nil {
				return err
			}
			if rootOpts.JSON {
				return writeJSON(cmd.OutOrStdout(), res)
			}
			printPixelWait(cmd, res)
			return nil
		},
	}
	cmd.Flags().StringVar(&region, "region", "", "region in screen coordinates as x,y,width,height (required)")
	cmd.Flags().Float64Var(&threshold, "threshold", wait.DefaultPixelThreshold, "dhash diff threshold in [0,1]")
	cmd.Flags().IntVar(&timeoutMS, "timeout-ms", wait.DefaultPixelTimeoutMS, "wait timeout in milliseconds")
	cmd.Flags().IntVar(&pollMS, "poll-ms", wait.DefaultPixelPollMS, "poll cadence in milliseconds")
	cmd.Flags().StringVar(&mode, "mode", wait.PixelModeAny, "any (default) or stable")
	cmd.Flags().IntVar(&stableMS, "stable-ms", wait.DefaultPixelStableMS, "required stable window in milliseconds (mode=stable)")
	return cmd
}

func newWaitForTextCommand() *cobra.Command {
	var (
		region        string
		present       = true
		timeoutMS     int
		pollMS        int
		minConfidence float64
		lang          string
		caseSensitive bool
		regex         bool
	)
	cmd := &cobra.Command{
		Use:   "wait-for-text [query]",
		Short: "Wait until OCR finds (or with --present=false stops finding) text matching the query",
		Args:  cobra.ExactArgs(1),
		Example: `  mycomputer wait-for-text "Sign in" --timeout-ms 8000 --json
  mycomputer wait-for-text "Loading" --present=false --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			req := wait.TextRequest{
				Query:         args[0],
				TimeoutMS:     timeoutMS,
				PollMS:        pollMS,
				MinConfidence: minConfidence,
				Lang:          lang,
				CaseSensitive: caseSensitive,
				Regex:         regex,
			}
			req.Present = &present
			if region != "" {
				b, err := parseBounds(region)
				if err != nil {
					return err
				}
				req.Region = contract.RegionRefFromBounds(b)
			}
			res, err := pipeline.RunWaitForText(cmd.Context(), req)
			if err != nil {
				return err
			}
			if rootOpts.JSON {
				return writeJSON(cmd.OutOrStdout(), res)
			}
			printTextWait(cmd, res)
			return nil
		},
	}
	cmd.Flags().StringVar(&region, "region", "", "search region as x,y,width,height (defaults to focused window or full screen)")
	cmd.Flags().BoolVar(&present, "present", true, "wait for the text to be present (default) or absent (--present=false)")
	cmd.Flags().IntVar(&timeoutMS, "timeout-ms", wait.DefaultTextTimeoutMS, "wait timeout in milliseconds")
	cmd.Flags().IntVar(&pollMS, "poll-ms", wait.DefaultTextPollMS, "poll cadence in milliseconds (keep >= 200 due to OCR cost)")
	cmd.Flags().Float64Var(&minConfidence, "min-confidence", wait.DefaultTextMinConfidence, "drop OCR candidates below this confidence (0..1)")
	cmd.Flags().StringVar(&lang, "lang", "eng", "Tesseract language code")
	cmd.Flags().BoolVar(&caseSensitive, "case-sensitive", false, "match query case-sensitively")
	cmd.Flags().BoolVar(&regex, "regex", false, "interpret query as a regular expression")
	return cmd
}

func printWindowWait(cmd *cobra.Command, res wait.WindowResult) {
	w := cmd.OutOrStdout()
	if res.Matched != nil {
		_, _ = fmt.Fprintf(w, "matched\t%s\t%s\t%s\n", res.Matched.ID, res.Matched.Class, res.Matched.Title)
	} else {
		_, _ = fmt.Fprintln(w, "matched\t(absent)")
	}
	_, _ = fmt.Fprintf(w, "polls\t%d\n", res.Polls)
	_, _ = fmt.Fprintf(w, "elapsed_ms\t%d\n", res.ElapsedMS)
}

func printPixelWait(cmd *cobra.Command, res wait.PixelResult) {
	w := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(w, "changed\t%v\n", res.Changed)
	_, _ = fmt.Fprintf(w, "diff\t%.4f\n", res.Diff)
	_, _ = fmt.Fprintf(w, "polls\t%d\n", res.Polls)
	_, _ = fmt.Fprintf(w, "elapsed_ms\t%d\n", res.ElapsedMS)
}

func printTextWait(cmd *cobra.Command, res wait.TextResult) {
	w := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(w, "found\t%v\n", res.Found)
	if res.Candidate != nil {
		_, _ = fmt.Fprintf(w, "  bounds: %d,%d,%dx%d  conf=%.3f\n", res.Candidate.Bounds.X, res.Candidate.Bounds.Y, res.Candidate.Bounds.Width, res.Candidate.Bounds.Height, res.Candidate.Confidence)
	}
	_, _ = fmt.Fprintf(w, "polls\t%d\n", res.Polls)
	_, _ = fmt.Fprintf(w, "elapsed_ms\t%d\n", res.ElapsedMS)
}
