package main

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/screen"
)

func newCaptureCommand() *cobra.Command {
	var req screen.CaptureRequest
	var region string
	var zoom string
	cmd := &cobra.Command{
		Use:   "capture",
		Short: "Capture full screen, region, or zoom crop",
		Example: `  mycomputer capture --out /tmp/shot.png --json
  mycomputer capture --region 0,0,800,600 --out /tmp/region.png
  mycomputer capture --zoom 500,300,400 --json
  mycomputer capture --format jpeg --cursor --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if region != "" {
				bounds, err := parseBounds(region)
				if err != nil {
					return err
				}
				// CLI --region is screen-space by definition; wrap the
				// parsed Bounds in a RegionRef with empty Space so it
				// flows through ResolveRegion as a no-op (v0.1/v0.2
				// behavior preserved).
				req.Region = contract.RegionRefFromBounds(bounds)
			}
			if zoom != "" {
				x, y, size, err := parseZoom(zoom)
				if err != nil {
					return err
				}
				req.Zoom = true
				req.ZoomX = x
				req.ZoomY = y
				req.ZoomSize = size
			}
			if req.MaxEdge == 0 {
				req.MaxEdge = 1568
			}
			result, err := screen.Capture(cmd.Context(), req)
			if err != nil {
				return err
			}
			if rootOpts.JSON {
				return writeJSON(cmd.OutOrStdout(), result)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", result.ImagePath, result.MimeType, result.CoordMap)
			return nil
		},
	}
	cmd.Flags().StringVar(&req.Out, "out", "", "write screenshot image to path")
	cmd.Flags().StringVar(&region, "region", "", "capture region as x,y,width,height")
	cmd.Flags().StringVar(&zoom, "zoom", "", "capture zoom crop as center_x,center_y,size")
	cmd.Flags().IntVar(&req.MaxEdge, "max-edge", 1568, "downscale output so the longest edge is at most N pixels (0 disables)")
	cmd.Flags().StringVar(&req.Format, "format", "png", "image format: png or jpeg")
	cmd.Flags().BoolVar(&req.Cursor, "cursor", false, "overlay the current cursor when XFixes is available")
	cmd.Flags().IntVar(&req.JPEGQuality, "jpeg-quality", 85, "JPEG quality from 1 to 100")
	return cmd
}

func parseBounds(value string) (contract.Bounds, error) {
	parts := splitCSV(value)
	if len(parts) != 4 {
		return contract.Bounds{}, contract.Validation("INVALID_REGION", "region must be x,y,width,height", map[string]any{"region": value})
	}
	ints := make([]int, len(parts))
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return contract.Bounds{}, contract.Validation("INVALID_REGION", "region contains a non-integer value", map[string]any{"region": value, "part": part})
		}
		ints[i] = n
	}
	if ints[2] <= 0 || ints[3] <= 0 {
		return contract.Bounds{}, contract.Validation("INVALID_REGION", "region width and height must be positive", map[string]any{"region": value})
	}
	return contract.Bounds{X: ints[0], Y: ints[1], Width: ints[2], Height: ints[3]}, nil
}

func parseZoom(value string) (int, int, int, error) {
	parts := splitCSV(value)
	if len(parts) != 3 {
		return 0, 0, 0, contract.Validation("INVALID_ZOOM", "zoom must be center_x,center_y,size", map[string]any{"zoom": value})
	}
	var ints [3]int
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return 0, 0, 0, contract.Validation("INVALID_ZOOM", "zoom contains a non-integer value", map[string]any{"zoom": value, "part": part})
		}
		ints[i] = n
	}
	if ints[2] <= 0 {
		return 0, 0, 0, contract.Validation("INVALID_ZOOM", "zoom size must be positive", map[string]any{"zoom": value})
	}
	return ints[0], ints[1], ints[2], nil
}
