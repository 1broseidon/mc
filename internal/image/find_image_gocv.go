//go:build gocv

package imageutil

import (
	"context"
	"image"

	"gocv.io/x/gocv"

	"github.com/1broseidon/mc/internal/contract"
)

// templateMatch is the OpenCV-backed implementation. Honors multi-scale.
// Build with: go build -tags gocv ./...
// Requires gocv (github.com/hybridgroup/gocv) and OpenCV 4 libs.
func templateMatch(ctx context.Context, haystack, template *image.RGBA, scales []float64, threshold float64) ([]contract.FindCandidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, contract.Cancelled("template match cancelled")
	}
	hMat, err := gocv.ImageToMatRGBA(haystack)
	if err != nil {
		return nil, contract.Dependency("OPENCV_CONVERT_FAILED", "failed to convert haystack to OpenCV mat", map[string]any{"error": err.Error()})
	}
	defer hMat.Close()
	tMat, err := gocv.ImageToMatRGBA(template)
	if err != nil {
		return nil, contract.Dependency("OPENCV_CONVERT_FAILED", "failed to convert template to OpenCV mat", map[string]any{"error": err.Error()})
	}
	defer tMat.Close()

	hGray := gocv.NewMat()
	defer hGray.Close()
	tGray := gocv.NewMat()
	defer tGray.Close()
	gocv.CvtColor(hMat, &hGray, gocv.ColorRGBAToGray)
	gocv.CvtColor(tMat, &tGray, gocv.ColorRGBAToGray)

	var cands []contract.FindCandidate
	for _, scale := range scales {
		if err := ctx.Err(); err != nil {
			return nil, contract.Cancelled("template match cancelled")
		}
		scaled := gocv.NewMat()
		newW := int(float64(tGray.Cols()) * scale)
		newH := int(float64(tGray.Rows()) * scale)
		if newW <= 0 || newH <= 0 || newW > hGray.Cols() || newH > hGray.Rows() {
			scaled.Close()
			continue
		}
		gocv.Resize(tGray, &scaled, image.Pt(newW, newH), 0, 0, gocv.InterpolationLinear)

		result := gocv.NewMat()
		gocv.MatchTemplate(hGray, scaled, &result, gocv.TmCcoeffNormed, gocv.NewMat())

		// Walk the score map collecting maxima.
		rows := result.Rows()
		cols := result.Cols()
		for y := 0; y < rows; y++ {
			for x := 0; x < cols; x++ {
				v := result.GetFloatAt(y, x)
				if float64(v) >= threshold {
					cands = append(cands, contract.FindCandidate{
						Bounds:     contract.Bounds{X: x, Y: y, Width: newW, Height: newH},
						Confidence: float64(v),
						Source:     contract.FindSourceTemplate,
						Extra: map[string]any{
							"backend": "opencv",
							"scale":   scale,
						},
					})
				}
			}
		}
		result.Close()
		scaled.Close()
	}
	return nms(cands, 0.5), nil
}

func probeTemplateBackend() contract.BackendStatus {
	return contract.BackendStatus{
		Name:     "template_match",
		Ready:    true,
		Required: false,
		Message:  "opencv via gocv",
		Details: map[string]any{
			"backend":        "opencv",
			"opencv_version": gocv.OpenCVVersion(),
		},
		Capabilities: []string{"multi_scale"},
	}
}
