//go:build !gocv

package imageutil

import (
	"context"
	"image"
	"math"

	"github.com/1broseidon/mc/internal/contract"
)

// templateMatch is the pure-Go normalized-cross-correlation backend.
// Single-scale only (the first entry of scales is used; others are
// ignored with a note in Extra). Suitable for templates up to roughly
// 200x200 px on a 1080p screen — beyond that, install OpenCV and build
// with `-tags gocv`.
func templateMatch(ctx context.Context, haystack, template *image.RGBA, scales []float64, threshold float64) ([]contract.FindCandidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, contract.Cancelled("template match cancelled")
	}
	hw := haystack.Bounds().Dx()
	hh := haystack.Bounds().Dy()
	tw := template.Bounds().Dx()
	th := template.Bounds().Dy()
	if tw == 0 || th == 0 {
		return nil, contract.Validation("TEMPLATE_EMPTY", "template image is empty", nil)
	}
	if tw > hw || th > hh {
		return nil, contract.Validation("TEMPLATE_TOO_LARGE", "template image is larger than the search region", map[string]any{"template": []int{tw, th}, "region": []int{hw, hh}})
	}

	// Convert both to grayscale float arrays once.
	hGray := toGray(haystack)
	tGray := toGray(template)

	// Pre-compute template mean and zero-mean template.
	var tMean float64
	for _, v := range tGray {
		tMean += v
	}
	n := float64(len(tGray))
	tMean /= n
	tZero := make([]float64, len(tGray))
	var tDenom float64
	for i, v := range tGray {
		d := v - tMean
		tZero[i] = d
		tDenom += d * d
	}
	tDenom = math.Sqrt(tDenom)
	if tDenom == 0 {
		return nil, contract.Validation("TEMPLATE_UNIFORM", "template image has no variance (uniform color)", nil)
	}

	// NCC scan. To keep the pure-Go fallback feasible we step in one-pixel
	// strides; we suppress overlapping matches via simple non-maximum
	// suppression after collecting peaks above threshold.
	var raw []contract.FindCandidate
	for y := 0; y <= hh-th; y++ {
		if y%32 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, contract.Cancelled("template match cancelled")
			}
		}
		for x := 0; x <= hw-tw; x++ {
			// Compute mean of haystack window.
			var sum float64
			for dy := 0; dy < th; dy++ {
				row := (y+dy)*hw + x
				for dx := 0; dx < tw; dx++ {
					sum += hGray[row+dx]
				}
			}
			hMean := sum / n
			// Compute NCC numerator & denominator.
			var num, hDenom float64
			for dy := 0; dy < th; dy++ {
				row := (y+dy)*hw + x
				trow := dy * tw
				for dx := 0; dx < tw; dx++ {
					hd := hGray[row+dx] - hMean
					num += hd * tZero[trow+dx]
					hDenom += hd * hd
				}
			}
			hDenom = math.Sqrt(hDenom)
			if hDenom == 0 {
				continue
			}
			score := num / (hDenom * tDenom)
			if score >= threshold {
				raw = append(raw, contract.FindCandidate{
					Bounds:     contract.Bounds{X: x, Y: y, Width: tw, Height: th},
					Confidence: clamp01(score),
					Source:     contract.FindSourceTemplate,
					Extra: map[string]any{
						"backend": "pure_go",
						"scale":   1.0,
					},
				})
			}
		}
	}
	// Non-maximum suppression: drop any candidate that overlaps a better
	// one by more than 50% of its area.
	deduped := nms(raw, 0.5)
	if len(scales) > 1 {
		// Annotate that multi-scale was requested but not honored.
		for i := range deduped {
			if deduped[i].Extra == nil {
				deduped[i].Extra = map[string]any{}
			}
			deduped[i].Extra["note"] = "pure_go backend ignores additional scales; rebuild with -tags gocv for multi-scale"
		}
	}
	return deduped, nil
}

func probeTemplateBackend() contract.BackendStatus {
	return contract.BackendStatus{
		Name:     "template_match",
		Ready:    true,
		Required: false,
		Message:  "pure_go normalized-cross-correlation (single-scale)",
		Details: map[string]any{
			"backend": "pure_go",
		},
		Capabilities: []string{"single_scale"},
	}
}

func toGray(img *image.RGBA) []float64 {
	w := img.Bounds().Dx()
	h := img.Bounds().Dy()
	out := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := img.RGBAAt(x, y)
			// Rec. 601 luma.
			out[y*w+x] = 0.299*float64(c.R) + 0.587*float64(c.G) + 0.114*float64(c.B)
		}
	}
	return out
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// nms is a simple non-maximum-suppression pass keyed on bbox overlap.
func nms(cands []contract.FindCandidate, iouThreshold float64) []contract.FindCandidate {
	// Sort by confidence desc.
	for i := 1; i < len(cands); i++ {
		for j := i; j > 0 && cands[j].Confidence > cands[j-1].Confidence; j-- {
			cands[j], cands[j-1] = cands[j-1], cands[j]
		}
	}
	kept := make([]contract.FindCandidate, 0, len(cands))
	for _, c := range cands {
		drop := false
		for _, k := range kept {
			if iou(c.Bounds, k.Bounds) > iouThreshold {
				drop = true
				break
			}
		}
		if !drop {
			kept = append(kept, c)
		}
	}
	return kept
}

func iou(a, b contract.Bounds) float64 {
	ax2 := a.X + a.Width
	ay2 := a.Y + a.Height
	bx2 := b.X + b.Width
	by2 := b.Y + b.Height
	ix1 := maxInt(a.X, b.X)
	iy1 := maxInt(a.Y, b.Y)
	ix2 := minInt(ax2, bx2)
	iy2 := minInt(ay2, by2)
	iw := ix2 - ix1
	ih := iy2 - iy1
	if iw <= 0 || ih <= 0 {
		return 0
	}
	inter := float64(iw * ih)
	union := float64(a.Width*a.Height+b.Width*b.Height) - inter
	if union <= 0 {
		return 0
	}
	return inter / union
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
