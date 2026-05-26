package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/pipeline"
)

func newFindTextCommand() *cobra.Command {
	var region string
	action := pipeline.Action{Type: "find_text"}
	cmd := &cobra.Command{
		Use:   "find-text [query]",
		Short: "OCR a screen region and return candidates matching a query",
		Args:  cobra.ExactArgs(1),
		Example: `  mycomputer find-text "Trust" --json
  mycomputer find-text "Sign in" --region 0,0,800,600 --min-confidence 0.7 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			action.Query = args[0]
			if region != "" {
				b, err := parseBounds(region)
				if err != nil {
					return err
				}
				action.Region = contract.RegionRefFromBounds(b)
			}
			result, err := pipeline.RunFindText(cmd.Context(), action)
			if err != nil {
				return err
			}
			if rootOpts.JSON {
				return writeJSON(cmd.OutOrStdout(), result)
			}
			printFindResult(cmd, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&region, "region", "", "search region as x,y,width,height (default: focused window or full screen)")
	cmd.Flags().StringVar(&action.Lang, "lang", "eng", "Tesseract language code")
	cmd.Flags().BoolVar(&action.CaseSensitive, "case-sensitive", false, "match query case-sensitively")
	cmd.Flags().BoolVar(&action.Regex, "regex", false, "interpret query as a regular expression")
	cmd.Flags().Float64Var(&action.MinConfidence, "min-confidence", 0.0, "drop candidates below this OCR confidence (0..1)")
	cmd.Flags().StringVar(&action.Preprocess, "preprocess", "", "OCR preprocess mode: auto (default; auto-inverts dark-theme regions), invert, binarize, or none")
	cmd.Flags().IntVar(&action.PSM, "psm", 0, "Tesseract page segmentation mode 0..13; 0 omits the flag (use tesseract default)")
	cmd.Flags().IntVar(&action.OEM, "oem", 0, "Tesseract OCR engine mode 0..3; 0 omits the flag (use tesseract default)")
	return cmd
}

func newFindImageCommand() *cobra.Command {
	var region string
	var scales []float64
	action := pipeline.Action{Type: "find_image"}
	cmd := &cobra.Command{
		Use:   "find-image",
		Short: "Locate a template image on screen by template matching",
		Example: `  mycomputer find-image --template button.png --threshold 0.85 --json
  mycomputer find-image --template logo.png --region 0,0,1280,400 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if action.TemplatePath == "" {
				return contract.Validation("TEMPLATE_PATH_REQUIRED", "find-image requires --template", nil)
			}
			if region != "" {
				b, err := parseBounds(region)
				if err != nil {
					return err
				}
				action.Region = contract.RegionRefFromBounds(b)
			}
			action.Scales = scales
			result, err := pipeline.RunFindImage(cmd.Context(), action)
			if err != nil {
				return err
			}
			if rootOpts.JSON {
				return writeJSON(cmd.OutOrStdout(), result)
			}
			printFindResult(cmd, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&action.TemplatePath, "template", "", "path to template PNG or JPEG (required)")
	cmd.Flags().StringVar(&region, "region", "", "search region as x,y,width,height")
	cmd.Flags().Float64Var(&action.Threshold, "threshold", 0.9, "match threshold (0..1)")
	cmd.Flags().Float64SliceVar(&scales, "scales", []float64{1.0}, "scale factors to try (multi-scale requires -tags gocv)")
	return cmd
}

func newFindColorCommand() *cobra.Command {
	var region string
	var point string
	action := pipeline.Action{Type: "find_color"}
	cmd := &cobra.Command{
		Use:   "find-color [color]",
		Short: "Sample a pixel or find color blobs within tolerance",
		Args:  cobra.MaximumNArgs(1),
		Example: `  mycomputer find-color "#a78bfa" --json
  mycomputer find-color "#ff0000" --region 0,0,400,400 --tolerance 12 --json
  mycomputer find-color --point 500,300 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				action.Color = args[0]
			}
			if region != "" {
				b, err := parseBounds(region)
				if err != nil {
					return err
				}
				action.Region = contract.RegionRefFromBounds(b)
			}
			if point != "" {
				parts := splitCSV(point)
				if len(parts) != 2 {
					return contract.Validation("INVALID_POINT", "point must be x,y", map[string]any{"point": point})
				}
				var x, y int
				if _, err := fmt.Sscanf(parts[0], "%d", &x); err != nil {
					return contract.Validation("INVALID_POINT", "point x is not an integer", map[string]any{"point": point})
				}
				if _, err := fmt.Sscanf(parts[1], "%d", &y); err != nil {
					return contract.Validation("INVALID_POINT", "point y is not an integer", map[string]any{"point": point})
				}
				action.Point = contract.Point{X: x, Y: y, Space: contract.CoordSpaceScreen}
			}
			result, err := pipeline.RunFindColor(cmd.Context(), action)
			if err != nil {
				return err
			}
			if rootOpts.JSON {
				return writeJSON(cmd.OutOrStdout(), result)
			}
			printFindResult(cmd, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&region, "region", "", "search region as x,y,width,height")
	cmd.Flags().StringVar(&point, "point", "", "sample a single pixel as x,y (overrides blob search)")
	cmd.Flags().IntVar(&action.Tolerance, "tolerance", 8, "per-channel color tolerance")
	cmd.Flags().IntVar(&action.MinArea, "min-area", 4, "minimum blob area in pixels")
	return cmd
}

func printFindResult(cmd *cobra.Command, result contract.FindResult) {
	w := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(w, "candidates\t%d\n", len(result.Candidates))
	for i, c := range result.Candidates {
		_, _ = fmt.Fprintf(w, "  [%d] %.3f\t%d,%d,%dx%d\t%s\n", i, c.Confidence, c.Bounds.X, c.Bounds.Y, c.Bounds.Width, c.Bounds.Height, c.Source)
		if t, ok := c.Extra["text"].(string); ok {
			_, _ = fmt.Fprintf(w, "       text=%q\n", t)
		}
		if h, ok := c.Extra["hex"].(string); ok {
			_, _ = fmt.Fprintf(w, "       hex=%s\n", h)
		}
		if p, ok := c.Extra["preprocess"].(string); ok && p != "" {
			_, _ = fmt.Fprintf(w, "       preprocess=%s\n", p)
		}
	}
}
