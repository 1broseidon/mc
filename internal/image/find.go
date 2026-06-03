// Package imageutil hosts the pixel-driven targeting primitives:
// find_text (OCR via Tesseract), find_image (template match — gocv when
// the gocv build tag is set, pure-Go normalized cross-correlation
// otherwise), and find_color (pure-Go pixel and blob search).
//
// Build matrix:
//
//	default build         pure-Go template match (multi-scale disabled)
//	-tags gocv            gocv-based multi-scale template match
//
// Both modes are tested by CI. The default is pure-Go so the binary
// builds on machines without OpenCV; gocv is opt-in.
package imageutil

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/1broseidon/mc/internal/contract"
)

// BatchContext carries per-batch state shared across find_* primitives
// inside a single computer_actions batch. Today it caches OCR results
// keyed by region + pixel-hash so repeated find_text calls in the same
// batch do not re-shell-out to tesseract. Lifetime is exactly one batch;
// callers MUST allocate a fresh BatchContext per pipeline.Run.
//
// The zero value is NOT safe — use NewBatchContext.
type BatchContext struct {
	mu       sync.Mutex
	ocr      map[string]ocrEntry
	ocrCalls int
	// Slots stores named find results produced by actions with an "as"
	// field, so a later click/move can reference them via target_slot.
	slots map[string]contract.FindCandidate
}

// NewBatchContext returns an empty BatchContext ready for use.
func NewBatchContext() *BatchContext {
	return &BatchContext{
		ocr:   map[string]ocrEntry{},
		slots: map[string]contract.FindCandidate{},
	}
}

// OCRCalls reports how many times the Tesseract subprocess was actually
// invoked through this batch context. Exposed for tests (batch-cache
// hit counter).
func (b *BatchContext) OCRCalls() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ocrCalls
}

// SetSlot stores a named find candidate so later actions in the batch
// can target it via target_slot.
func (b *BatchContext) SetSlot(name string, c contract.FindCandidate) {
	if b == nil || name == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.slots[name] = c
}

// Slot retrieves a previously stored named candidate.
func (b *BatchContext) Slot(name string) (contract.FindCandidate, bool) {
	if b == nil || name == "" {
		return contract.FindCandidate{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.slots[name]
	return c, ok
}

type ocrEntry struct {
	words []ocrWord
}

type ocrWord struct {
	text       string
	left, top  int
	width      int
	height     int
	confidence float64 // 0..1
	// Tesseract groups tokens hierarchically: page → block → paragraph →
	// line → word. Phrase grouping requires the (block, par, line)
	// triple so we only concatenate tokens that Tesseract itself placed
	// on the same line. This avoids spurious phrases spanning columns
	// or wrapped lines (the latter is out-of-scope for v0.3 — see
	// generatePhraseCandidates).
	block int
	par   int
	line  int
}

// --- find_text -------------------------------------------------------

// FindTextRequest carries the inputs for find_text. Region is in screen
// coordinates; an empty region defaults to the full screen at the call
// site (callers that want focused-window default supply the bounds).
//
// Preprocess selects the pixel-pipeline applied before Tesseract:
//   - "" or "auto" — sample region mean luminance; if below
//     darkThemeLuminanceThreshold invert the image (white text on dark
//     becomes black on white, which Tesseract handles natively).
//   - "invert"     — unconditionally invert RGB channels.
//   - "binarize"   — Otsu thresholding to pure black/white.
//   - "none"       — pass pixels through unchanged.
//
// PSM and OEM are Tesseract tunables: any non-zero value is forwarded
// as `--psm <n>` / `--oem <n>`. Zero (the default) omits the flag so
// Tesseract uses its own defaults. This keeps the OCR output bit-for-bit
// identical for callers that don't opt in.
type FindTextRequest struct {
	Region        contract.Bounds `json:"region,omitzero"`
	Query         string          `json:"query"`
	Lang          string          `json:"lang,omitempty"`
	CaseSensitive bool            `json:"case_sensitive,omitempty"`
	MinConfidence float64         `json:"min_confidence,omitempty"`
	Regex         bool            `json:"regex,omitempty"`
	Preprocess    string          `json:"preprocess,omitempty"`
	PSM           int             `json:"psm,omitempty"`
	OEM           int             `json:"oem,omitempty"`
}

// darkThemeLuminanceThreshold is the mean-luminance cutoff (in the
// normalized 0..1 range) below which the "auto" preprocess pipeline
// considers the region a dark-themed UI and inverts the pixels before
// OCR. 80/255 ~= 0.314 was picked empirically against gnome-calculator,
// gnome-terminal, and Electron dark themes — well below any printed
// document or light-mode app while above the noise on plain black
// backgrounds. Tweak here; the constant is intentionally not exposed
// on the wire to keep the contract surface minimal.
const darkThemeLuminanceThreshold = 80.0 / 255.0

// smartPSMRegionThresholdPx is the upper area limit (in pixels) below
// which find_text will auto-retry with psm=11 (sparse text) when the
// default-PSM pass returns zero candidates. Below this size, the region
// is likely a button label or sparse label, where psm=11 produces better
// results than the default page-segmentation. Larger regions are likely
// paragraph layouts where the default PSM is already optimal, so we
// don't pay the extra Tesseract invocation. The auto-retry only fires
// when the caller did not pass an explicit psm — explicit psm preserves
// bit-for-bit existing behavior.
const smartPSMRegionThresholdPx = 100_000

// smartRetryPSM is the PSM value used for the sparse-text auto-retry
// when the default pass returns zero candidates on a tight region.
// PSM 11 is Tesseract's "sparse text — find as much text as possible
// in no particular order" mode, which dramatically outperforms the
// default page-segmentation on button labels, icon captions, and other
// non-paragraph UI text.
const smartRetryPSM = 11

// FindText runs Tesseract over the requested region and returns
// candidates whose recognized text matches the query. Sort order:
// confidence desc, then top-to-bottom, left-to-right.
//
// Caching: when batch is non-nil, the OCR result for a given region +
// pixel hash is cached for the duration of the batch.
//
// Errors:
//   - DEPENDENCY_UNAVAILABLE (exit 4): tesseract binary missing.
//   - TARGET_NOT_FOUND (exit 3): zero candidates matched the query.
func FindText(ctx context.Context, batch *BatchContext, region contract.Bounds, img *image.RGBA, req FindTextRequest) (contract.FindResult, error) {
	if req.Query == "" {
		return contract.FindResult{}, contract.Validation("QUERY_REQUIRED", "find_text requires a query", nil)
	}
	if img == nil {
		return contract.FindResult{}, contract.Validation("IMAGE_REQUIRED", "find_text requires a pixel buffer", nil)
	}
	lang := strings.TrimSpace(req.Lang)
	if lang == "" {
		lang = "eng"
	}

	mode := strings.ToLower(strings.TrimSpace(req.Preprocess))
	if mode == "" {
		mode = "auto"
	}
	switch mode {
	case "auto", "invert", "binarize", "none":
	default:
		return contract.FindResult{}, contract.Validation("PREPROCESS_INVALID", "preprocess must be one of auto, invert, binarize, none", map[string]any{"preprocess": req.Preprocess})
	}
	prepped, applied := preprocessImage(img, mode)

	// psmUsed tracks the PSM value applied to the OCR call that
	// produced the returned words. It starts at req.PSM (0 == Tesseract
	// default) and is bumped to smartRetryPSM by the auto-retry path
	// below. psmRetried records whether that retry fired so we can
	// surface it in each candidate's extra.
	psmUsed := req.PSM
	psmRetried := false
	words, err := runOCR(ctx, batch, region, prepped, lang, applied, psmUsed, req.OEM)
	if err != nil {
		if shouldSmartRetryOCR(region, req.PSM) && isOCROutputMissing(err) {
			retryWords, retryErr := runOCR(ctx, batch, region, prepped, lang, applied, smartRetryPSM, req.OEM)
			if retryErr == nil {
				words = retryWords
				psmUsed = smartRetryPSM
				psmRetried = true
			} else {
				return contract.FindResult{}, err
			}
		} else {
			return contract.FindResult{}, err
		}
	}

	minConf := req.MinConfidence
	if minConf <= 0 {
		minConf = 0.0
	}

	var matcher func(string) bool
	if req.Regex {
		flags := ""
		if !req.CaseSensitive {
			flags = "(?i)"
		}
		re, err := regexp.Compile(flags + req.Query)
		if err != nil {
			return contract.FindResult{}, contract.Validation("QUERY_INVALID_REGEX", "find_text query is not a valid regular expression", map[string]any{"query": req.Query, "error": err.Error()})
		}
		matcher = re.MatchString
	} else {
		needle := req.Query
		if !req.CaseSensitive {
			needle = strings.ToLower(needle)
		}
		matcher = func(s string) bool {
			if !req.CaseSensitive {
				s = strings.ToLower(s)
			}
			return strings.Contains(s, needle)
		}
	}

	// Phrase mode activates when the query references more than one
	// OCR token: either contains whitespace literally, or is a regex
	// whose pattern matches whitespace (`\s`). In phrase mode we
	// generate n-gram candidates from consecutive tokens on the same
	// (block, par, line) so the matcher can hit "Message Familiar"
	// against the joined text. For single-token literal queries we
	// fall through to the legacy per-token path so the existing
	// candidate stream is byte-identical to v0.2.
	multiwordMode := strings.ContainsAny(req.Query, " \t\n") ||
		(req.Regex && strings.Contains(req.Query, `\s`))

	buildUnits := func(ws []ocrWord) []matchUnit {
		units := tokenMatchUnits(ws)
		if multiwordMode {
			units = append(units, phraseMatchUnits(ws, maxPhraseTokens)...)
		}
		return units
	}

	buildCandidates := func(units []matchUnit) []contract.FindCandidate {
		var out []contract.FindCandidate
		for _, u := range units {
			if strings.TrimSpace(u.text) == "" {
				continue
			}
			if u.confidence < minConf {
				continue
			}
			if !matcher(u.text) {
				continue
			}
			extra := map[string]any{
				"text":       u.text,
				"lang":       lang,
				"preprocess": applied,
			}
			if u.tokenCount > 1 {
				extra["tokens"] = u.tokenCount
			}
			if psmUsed != 0 {
				extra["psm"] = psmUsed
			}
			if req.OEM != 0 {
				extra["oem"] = req.OEM
			}
			if psmRetried {
				extra["psm_retried"] = true
				extra["psm_used"] = psmUsed
			}
			out = append(out, contract.FindCandidate{
				Bounds: contract.Bounds{
					X:      region.X + u.left,
					Y:      region.Y + u.top,
					Width:  u.width,
					Height: u.height,
				},
				Confidence: u.confidence,
				Source:     contract.FindSourceOCR,
				Extra:      extra,
			})
		}
		return out
	}

	cands := buildCandidates(buildUnits(words))

	// Smart-PSM auto-retry. Gate: caller passed default psm (req.PSM == 0)
	// AND first invocation produced zero matching candidates AND the
	// search region is small enough to plausibly be a button label
	// (area < smartPSMRegionThresholdPx). On gate match, retry the OCR
	// call with psm=11 (sparse text). If the retry yields candidates,
	// each surfaces psm_retried=true and psm_used=11 in extra. If the
	// retry still produces zero, we fall through to the canonical
	// TARGET_NOT_FOUND path unchanged. Explicit psm disables the retry
	// entirely so existing-caller output stays bit-for-bit identical.
	if !psmRetried && len(cands) == 0 && shouldSmartRetryOCR(region, req.PSM) {
		retryWords, retryErr := runOCR(ctx, batch, region, prepped, lang, applied, smartRetryPSM, req.OEM)
		if retryErr == nil {
			psmUsed = smartRetryPSM
			psmRetried = true
			cands = buildCandidates(buildUnits(retryWords))
		}
	}
	sortCandidates(cands)
	return contract.FindResult{
		Candidates:   cands,
		SearchRegion: region,
		CoordSpace:   contract.CoordSpaceScreen,
	}, nil
}

func shouldSmartRetryOCR(region contract.Bounds, psm int) bool {
	regionArea := region.Width * region.Height
	return psm == 0 && regionArea > 0 && regionArea < smartPSMRegionThresholdPx
}

func isOCROutputMissing(err error) bool {
	var app *contract.AppError
	if !errors.As(err, &app) || app.Code != "OCR_RUN_FAILED" {
		return false
	}
	reason, _ := app.Details["reason"].(string)
	return reason == "output_missing"
}

// runOCR shells out to tesseract with a temp PNG, returning recognized
// words with bounds (relative to the input image) and confidences in
// [0, 1]. Cached via BatchContext when non-nil.
//
// The cache key combines region+pixel-hash with lang+applied-preprocess
// +psm+oem so different OCR settings against the same pixels don't
// false-hit each other. `applied` describes the preprocessing actually
// performed ("none", "invert", or "binarize") and is used purely as a
// cache-key disambiguator here — the image bytes already reflect the
// preprocessing.
func runOCR(ctx context.Context, batch *BatchContext, region contract.Bounds, img *image.RGBA, lang, applied string, psm, oem int) ([]ocrWord, error) {
	bin, err := exec.LookPath("tesseract")
	if err != nil {
		return nil, contract.Dependency("DEPENDENCY_UNAVAILABLE", "tesseract binary not found on PATH", map[string]any{
			"backend":     "ocr_tesseract",
			"remediation": "install tesseract (e.g. apt install tesseract-ocr tesseract-ocr-eng)",
			"error":       err.Error(),
		})
	}

	hash := hashImage(region, img)
	cacheKey := fmt.Sprintf("%s|%s|%s|psm=%d|oem=%d", lang, applied, hash, psm, oem)
	if batch != nil {
		batch.mu.Lock()
		if e, ok := batch.ocr[cacheKey]; ok {
			batch.mu.Unlock()
			return e.words, nil
		}
		batch.mu.Unlock()
	}

	tmpIn, err := os.CreateTemp("", "mycomputer-ocr-*.png")
	if err != nil {
		return nil, contract.Dependency("OCR_TEMP_FAILED", "failed to allocate OCR temp file", map[string]any{"error": err.Error()})
	}
	defer func() { _ = os.Remove(tmpIn.Name()) }()
	if err := png.Encode(tmpIn, img); err != nil {
		_ = tmpIn.Close()
		return nil, contract.Dependency("OCR_TEMP_FAILED", "failed to encode OCR input PNG", map[string]any{"error": err.Error()})
	}
	_ = tmpIn.Close()

	tmpOutDir, err := os.MkdirTemp("", "mycomputer-ocr-out-*")
	if err != nil {
		return nil, contract.Dependency("OCR_TEMP_FAILED", "failed to allocate OCR output directory", map[string]any{"error": err.Error()})
	}
	defer func() { _ = os.RemoveAll(tmpOutDir) }()
	tmpOutBase := filepath.Join(tmpOutDir, "out")

	args := []string{tmpIn.Name(), tmpOutBase, "-l", lang}
	if psm != 0 {
		args = append(args, "--psm", strconv.Itoa(psm))
	}
	if oem != 0 {
		args = append(args, "--oem", strconv.Itoa(oem))
	}
	args = append(args, "tsv")
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, contract.Dependency("OCR_RUN_FAILED", "tesseract execution failed", map[string]any{"reason": "tesseract_exit", "error": err.Error(), "stderr": stderr.String()})
	}

	tsv, err := os.ReadFile(tmpOutBase + ".tsv")
	if err != nil {
		return nil, contract.Dependency("OCR_RUN_FAILED", "tesseract output not found", map[string]any{"reason": "output_missing", "error": err.Error()})
	}
	words, err := parseTesseractTSV(tsv)
	if err != nil {
		return nil, err
	}
	if batch != nil {
		batch.mu.Lock()
		batch.ocr[cacheKey] = ocrEntry{words: words}
		batch.ocrCalls++
		batch.mu.Unlock()
	}
	return words, nil
}

// maxPhraseTokens caps the size of n-gram phrase candidates generated
// from a single OCR line. With N tokens on a line this bounds the
// generated phrase set at roughly N * maxPhraseTokens — preventing
// quadratic blowup on dense pages. Six tokens covers the vast majority
// of UI labels ("Save as new template", "Add to existing list", etc.);
// pathologically long queries fall back to regex anyway.
const maxPhraseTokens = 6

// matchUnit is the per-candidate record handed to the find_text matcher.
// It abstracts over single-token and multi-token phrases so the matching
// loop is identical for both. tokenCount > 1 marks a phrase candidate
// (used only to populate Extra["tokens"]).
type matchUnit struct {
	text       string
	left, top  int
	width      int
	height     int
	confidence float64
	tokenCount int
}

// tokenMatchUnits projects raw OCR tokens into matchUnit form. The
// returned slice is 1:1 with the input tokens (including empty/space
// tokens — the matcher skips them).
func tokenMatchUnits(words []ocrWord) []matchUnit {
	out := make([]matchUnit, 0, len(words))
	for _, w := range words {
		out = append(out, matchUnit{
			text:       w.text,
			left:       w.left,
			top:        w.top,
			width:      w.width,
			height:     w.height,
			confidence: w.confidence,
			tokenCount: 1,
		})
	}
	return out
}

// phraseMatchUnits generates n-gram phrase candidates from consecutive
// tokens sharing the same (block, par, line) — the unit at which
// Tesseract itself laid out text. For each starting token i, it emits
// phrases of length 2..maxN joining tokens with single spaces. Bounds
// are the union of constituent token boxes; confidence is the minimum
// of constituent confidences (conservative — a phrase is only as
// trustworthy as its weakest token).
//
// Limitation: phrases never span a line break. A label that wraps
// onto two visual lines remains unmatchable as a single phrase. This
// is the v0.3 contract; revisit if real-world UIs surface the need.
func phraseMatchUnits(words []ocrWord, maxN int) []matchUnit {
	if maxN < 2 {
		return nil
	}
	var out []matchUnit
	for i := 0; i < len(words); i++ {
		// Skip starting on empty tokens — they contribute no text.
		if strings.TrimSpace(words[i].text) == "" {
			continue
		}
		var (
			parts     []string
			minLeft   = words[i].left
			minTop    = words[i].top
			maxRight  = words[i].left + words[i].width
			maxBottom = words[i].top + words[i].height
			minConf   = words[i].confidence
		)
		parts = append(parts, words[i].text)
		for j := i + 1; j < len(words) && (j-i) < maxN; j++ {
			w := words[j]
			// Phrase boundary: stop at the first token that's on a
			// different line/paragraph/block. Subsequent tokens on the
			// next line are not part of this phrase.
			if w.block != words[i].block || w.par != words[i].par || w.line != words[i].line {
				break
			}
			if strings.TrimSpace(w.text) == "" {
				continue
			}
			parts = append(parts, w.text)
			if w.left < minLeft {
				minLeft = w.left
			}
			if w.top < minTop {
				minTop = w.top
			}
			if r := w.left + w.width; r > maxRight {
				maxRight = r
			}
			if b := w.top + w.height; b > maxBottom {
				maxBottom = b
			}
			if w.confidence < minConf {
				minConf = w.confidence
			}
			if len(parts) >= 2 {
				out = append(out, matchUnit{
					text:       strings.Join(parts, " "),
					left:       minLeft,
					top:        minTop,
					width:      maxRight - minLeft,
					height:     maxBottom - minTop,
					confidence: minConf,
					tokenCount: len(parts),
				})
			}
		}
	}
	return out
}

// parseTesseractTSV interprets tesseract's TSV output. Columns (v3+):
// level, page_num, block_num, par_num, line_num, word_num,
// left, top, width, height, conf, text
func parseTesseractTSV(data []byte) ([]ocrWord, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = '\t'
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	var out []ocrWord
	first := true
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, contract.Dependency("OCR_PARSE_FAILED", "failed to parse tesseract TSV", map[string]any{"error": err.Error()})
		}
		if first {
			first = false
			continue
		}
		if len(rec) < 12 {
			continue
		}
		level, _ := strconv.Atoi(rec[0])
		if level != 5 {
			continue // only word-level rows
		}
		block, _ := strconv.Atoi(rec[2])
		par, _ := strconv.Atoi(rec[3])
		line, _ := strconv.Atoi(rec[4])
		left, _ := strconv.Atoi(rec[6])
		top, _ := strconv.Atoi(rec[7])
		w, _ := strconv.Atoi(rec[8])
		h, _ := strconv.Atoi(rec[9])
		confRaw, _ := strconv.ParseFloat(rec[10], 64)
		if confRaw < 0 {
			confRaw = 0
		}
		text := rec[11]
		out = append(out, ocrWord{
			text:       text,
			left:       left,
			top:        top,
			width:      w,
			height:     h,
			confidence: confRaw / 100.0,
			block:      block,
			par:        par,
			line:       line,
		})
	}
	return out, nil
}

// preprocessImage applies the requested preprocessing mode to img and
// returns (possibly the same) image and the label of what actually
// fired. Modes:
//
//   - "none":     returns img unchanged, applied="none".
//   - "invert":   unconditionally inverts RGB channels, applied="invert".
//   - "binarize": Otsu thresholding to pure black/white, applied="binarize".
//   - "auto":     single-pass scan computes mean luminance; if below
//     darkThemeLuminanceThreshold, inverts (applied="invert"); otherwise
//     returns img unchanged (applied="none").
//
// Any unknown mode is treated as "none" (defense in depth — callers are
// validated upstream).
//
// Note: returns a copy when any pixel mutation happens, so the caller's
// buffer is never mutated. The cache-key + pixel-hash invariant in
// runOCR relies on the returned image's Pix bytes reflecting the
// preprocessing result (not the original).
func preprocessImage(img *image.RGBA, mode string) (*image.RGBA, string) {
	if img == nil {
		return img, "none"
	}
	switch mode {
	case "invert":
		return invertImage(img), "invert"
	case "binarize":
		return otsuBinarize(img), "binarize"
	case "auto":
		// Single-pass mean-luminance over Pix. RGBA strides are tightly
		// packed (4 bytes per pixel, no per-row padding for image.NewRGBA),
		// so we walk Pix directly. Rec.601 luma coefficients.
		pix := img.Pix
		if len(pix) < 4 {
			return img, "none"
		}
		var sum uint64
		var count uint64
		for i := 0; i+3 < len(pix); i += 4 {
			r := uint32(pix[i])
			g := uint32(pix[i+1])
			b := uint32(pix[i+2])
			// 0.299 R + 0.587 G + 0.114 B, fixed-point ×1000.
			y := (299*r + 587*g + 114*b) / 1000
			sum += uint64(y)
			count++
		}
		if count == 0 {
			return img, "none"
		}
		meanLuma := float64(sum) / float64(count) / 255.0
		if meanLuma < darkThemeLuminanceThreshold {
			return invertImage(img), "invert"
		}
		return img, "none"
	default:
		return img, "none"
	}
}

// invertImage returns a new RGBA with each pixel's RGB channels
// inverted (alpha preserved). Walks Pix directly for speed.
func invertImage(img *image.RGBA) *image.RGBA {
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	// When the input has a zero origin and matches stride, we can copy
	// then flip in place. Use the safe per-pixel path otherwise.
	if img.Stride == out.Stride && b.Min.X == 0 && b.Min.Y == 0 {
		copy(out.Pix, img.Pix)
		pix := out.Pix
		for i := 0; i+3 < len(pix); i += 4 {
			pix[i] = 255 - pix[i]
			pix[i+1] = 255 - pix[i+1]
			pix[i+2] = 255 - pix[i+2]
		}
		return out
	}
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			c := img.RGBAAt(b.Min.X+x, b.Min.Y+y)
			out.SetRGBA(x, y, color.RGBA{R: 255 - c.R, G: 255 - c.G, B: 255 - c.B, A: c.A})
		}
	}
	return out
}

// otsuBinarize converts img to pure black/white using Otsu's method.
// Builds a 256-bin luminance histogram, finds the threshold that
// maximizes between-class variance, then maps each pixel to 0 or 255.
// The result is grayscale-as-RGBA (R=G=B), alpha preserved.
func otsuBinarize(img *image.RGBA) *image.RGBA {
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	pix := img.Pix
	if len(pix) < 4 {
		return out
	}
	var hist [256]uint64
	for i := 0; i+3 < len(pix); i += 4 {
		y := (299*uint32(pix[i]) + 587*uint32(pix[i+1]) + 114*uint32(pix[i+2])) / 1000
		if y > 255 {
			y = 255
		}
		hist[y]++
	}
	total := uint64(b.Dx() * b.Dy())
	if total == 0 {
		return out
	}
	var sum float64
	for t := 0; t < 256; t++ {
		sum += float64(t) * float64(hist[t])
	}
	var (
		sumB      float64
		wB, wF    uint64
		maxVar    float64
		threshold int
	)
	for t := 0; t < 256; t++ {
		wB += hist[t]
		if wB == 0 {
			continue
		}
		wF = total - wB
		if wF == 0 {
			break
		}
		sumB += float64(t) * float64(hist[t])
		mB := sumB / float64(wB)
		mF := (sum - sumB) / float64(wF)
		v := float64(wB) * float64(wF) * (mB - mF) * (mB - mF)
		if v > maxVar {
			maxVar = v
			threshold = t
		}
	}
	thr := uint32(threshold)
	outPix := out.Pix
	for i := 0; i+3 < len(pix); i += 4 {
		y := (299*uint32(pix[i]) + 587*uint32(pix[i+1]) + 114*uint32(pix[i+2])) / 1000
		var v uint8
		if y > thr {
			v = 255
		}
		outPix[i] = v
		outPix[i+1] = v
		outPix[i+2] = v
		outPix[i+3] = pix[i+3]
	}
	return out
}

// hashImage computes a short stable hash of the region bounds and pixel
// data for use as an OCR cache key.
func hashImage(region contract.Bounds, img *image.RGBA) string {
	h := sha1.New()
	_, _ = fmt.Fprintf(h, "%d,%d,%d,%d|", region.X, region.Y, region.Width, region.Height)
	h.Write(img.Pix)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// --- find_image ------------------------------------------------------

// FindImageRequest carries inputs for find_image template matching.
// Threshold defaults to 0.9 if zero. Scales defaults to []float{1.0};
// the gocv backend honors multi-scale, the pure-Go backend uses scale
// 1.0 only and logs a notice in Extra.
type FindImageRequest struct {
	TemplatePath string          `json:"template_path"`
	Region       contract.Bounds `json:"region,omitzero"`
	Threshold    float64         `json:"threshold,omitempty"`
	Scales       []float64       `json:"scales,omitempty"`
}

// FindImage performs template matching against the captured screen
// image. The actual backend (gocv vs pure-Go) is selected at build time.
// Both implementations honor the same FindImageRequest shape.
func FindImage(ctx context.Context, region contract.Bounds, img *image.RGBA, req FindImageRequest) (contract.FindResult, error) {
	if req.TemplatePath == "" {
		return contract.FindResult{}, contract.Validation("TEMPLATE_PATH_REQUIRED", "find_image requires template_path", nil)
	}
	if img == nil {
		return contract.FindResult{}, contract.Validation("IMAGE_REQUIRED", "find_image requires a pixel buffer", nil)
	}
	template, err := loadTemplate(req.TemplatePath)
	if err != nil {
		return contract.FindResult{}, err
	}
	threshold := req.Threshold
	if threshold <= 0 {
		threshold = 0.9
	}
	scales := req.Scales
	if len(scales) == 0 {
		scales = []float64{1.0}
	}
	cands, err := templateMatch(ctx, img, template, scales, threshold)
	if err != nil {
		return contract.FindResult{}, err
	}
	for i := range cands {
		cands[i].Bounds.X += region.X
		cands[i].Bounds.Y += region.Y
		cands[i].Source = contract.FindSourceTemplate
		if cands[i].Extra == nil {
			cands[i].Extra = map[string]any{}
		}
		cands[i].Extra["template_path"] = req.TemplatePath
	}
	sortCandidates(cands)
	return contract.FindResult{
		Candidates:   cands,
		SearchRegion: region,
		CoordSpace:   contract.CoordSpaceScreen,
	}, nil
}

func loadTemplate(path string) (*image.RGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, contract.NotFound("TEMPLATE_NOT_FOUND", "template image could not be opened", map[string]any{"template_path": path, "error": err.Error()})
	}
	defer func() { _ = f.Close() }()
	src, _, err := image.Decode(f)
	if err != nil {
		// Try PNG explicitly — image.Decode requires registered formats
		// and our build registers PNG via the import in this file.
		return nil, contract.Validation("TEMPLATE_DECODE_FAILED", "template image could not be decoded", map[string]any{"template_path": path, "error": err.Error()})
	}
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			dst.Set(x, y, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst, nil
}

// --- find_color ------------------------------------------------------

// FindColorRequest accepts either a Point sample mode or a Color blob
// search mode. The two modes are mutually exclusive; if both are set,
// Point wins because it is the more specific query.
type FindColorRequest struct {
	Point     *contract.Point `json:"point,omitempty"`
	Color     string          `json:"color,omitempty"`
	Region    contract.Bounds `json:"region,omitzero"`
	Tolerance int             `json:"tolerance,omitempty"`
	MinArea   int             `json:"min_area,omitempty"`
}

// FindColor either samples one pixel (Point mode) or searches a region
// for contiguous blobs of pixels within tolerance of the target color.
// Returned bounds are in screen coordinates.
func FindColor(ctx context.Context, region contract.Bounds, img *image.RGBA, req FindColorRequest) (contract.FindResult, error) {
	if img == nil {
		return contract.FindResult{}, contract.Validation("IMAGE_REQUIRED", "find_color requires a pixel buffer", nil)
	}
	// Point sample mode
	if req.Point != nil {
		px, py, err := contract.Resolve(*req.Point, contract.ResolveContext{})
		if err != nil {
			return contract.FindResult{}, err
		}
		lx := px - region.X
		ly := py - region.Y
		if lx < 0 || ly < 0 || lx >= img.Bounds().Dx() || ly >= img.Bounds().Dy() {
			return contract.FindResult{}, contract.Validation("POINT_OUT_OF_BOUNDS", "sample point is outside the capture region", map[string]any{"x": px, "y": py, "region": region})
		}
		c := img.RGBAAt(lx, ly)
		hex := fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
		cand := contract.FindCandidate{
			Bounds:     contract.Bounds{X: px, Y: py, Width: 1, Height: 1},
			Confidence: 1.0,
			Source:     contract.FindSourceColor,
			Extra: map[string]any{
				"mode":  "point",
				"hex":   hex,
				"rgba":  []int{int(c.R), int(c.G), int(c.B), int(c.A)},
				"alpha": int(c.A),
			},
		}
		return contract.FindResult{
			Candidates:   []contract.FindCandidate{cand},
			SearchRegion: region,
			CoordSpace:   contract.CoordSpaceScreen,
		}, nil
	}

	if req.Color == "" {
		return contract.FindResult{}, contract.Validation("COLOR_REQUIRED", "find_color requires either point or color", nil)
	}
	target, err := parseHexColor(req.Color)
	if err != nil {
		return contract.FindResult{}, err
	}
	tol := req.Tolerance
	if tol <= 0 {
		tol = 8
	}
	minArea := req.MinArea
	if minArea <= 0 {
		minArea = 4
	}

	w := img.Bounds().Dx()
	h := img.Bounds().Dy()
	mask := make([]bool, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := img.RGBAAt(x, y)
			if abs(int(c.R)-int(target.R)) <= tol &&
				abs(int(c.G)-int(target.G)) <= tol &&
				abs(int(c.B)-int(target.B)) <= tol {
				mask[y*w+x] = true
			}
		}
	}

	// Connected components (4-connected, iterative flood fill).
	visited := make([]bool, w*h)
	type blob struct{ minX, minY, maxX, maxY, area int }
	var blobs []blob
	stack := make([]int, 0, 64)
	for i := range mask {
		if !mask[i] || visited[i] {
			continue
		}
		stack = stack[:0]
		stack = append(stack, i)
		b := blob{minX: w, minY: h, maxX: -1, maxY: -1}
		for len(stack) > 0 {
			idx := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if visited[idx] {
				continue
			}
			visited[idx] = true
			x := idx % w
			y := idx / w
			b.area++
			if x < b.minX {
				b.minX = x
			}
			if y < b.minY {
				b.minY = y
			}
			if x > b.maxX {
				b.maxX = x
			}
			if y > b.maxY {
				b.maxY = y
			}
			if x > 0 && mask[idx-1] && !visited[idx-1] {
				stack = append(stack, idx-1)
			}
			if x+1 < w && mask[idx+1] && !visited[idx+1] {
				stack = append(stack, idx+1)
			}
			if y > 0 && mask[idx-w] && !visited[idx-w] {
				stack = append(stack, idx-w)
			}
			if y+1 < h && mask[idx+w] && !visited[idx+w] {
				stack = append(stack, idx+w)
			}
		}
		if b.area >= minArea {
			blobs = append(blobs, b)
		}
	}

	totalPixels := float64(w * h)
	if totalPixels == 0 {
		totalPixels = 1
	}
	cands := make([]contract.FindCandidate, 0, len(blobs))
	for _, b := range blobs {
		cands = append(cands, contract.FindCandidate{
			Bounds: contract.Bounds{
				X:      region.X + b.minX,
				Y:      region.Y + b.minY,
				Width:  b.maxX - b.minX + 1,
				Height: b.maxY - b.minY + 1,
			},
			Confidence: math.Min(1.0, float64(b.area)/totalPixels*100),
			Source:     contract.FindSourceColor,
			Extra: map[string]any{
				"mode":      "blob",
				"hex":       strings.ToLower(req.Color),
				"area":      b.area,
				"tolerance": tol,
			},
		})
	}
	sortCandidates(cands)
	return contract.FindResult{
		Candidates:   cands,
		SearchRegion: region,
		CoordSpace:   contract.CoordSpaceScreen,
	}, nil
}

func parseHexColor(s string) (color.RGBA, error) {
	v := strings.TrimSpace(s)
	v = strings.TrimPrefix(v, "#")
	if len(v) == 3 {
		v = string([]byte{v[0], v[0], v[1], v[1], v[2], v[2]})
	}
	if len(v) != 6 {
		return color.RGBA{}, contract.Validation("INVALID_COLOR", "color must be #rrggbb or #rgb", map[string]any{"color": s})
	}
	n, err := strconv.ParseUint(v, 16, 32)
	if err != nil {
		return color.RGBA{}, contract.Validation("INVALID_COLOR", "color is not a valid hex value", map[string]any{"color": s})
	}
	return color.RGBA{R: uint8(n >> 16), G: uint8(n >> 8), B: uint8(n), A: 255}, nil
}

// --- helpers ---------------------------------------------------------

func sortCandidates(cands []contract.FindCandidate) {
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Confidence != cands[j].Confidence {
			return cands[i].Confidence > cands[j].Confidence
		}
		if cands[i].Bounds.Y != cands[j].Bounds.Y {
			return cands[i].Bounds.Y < cands[j].Bounds.Y
		}
		return cands[i].Bounds.X < cands[j].Bounds.X
	})
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// --- doctor probes ---------------------------------------------------

// ProbeOCRTesseract reports tesseract availability for the doctor table.
// Required:false — find_text fails with DEPENDENCY_UNAVAILABLE when this
// is the only missing backend.
//
// Details surfaced (when available):
//   - binary:          absolute path
//   - version:         first line of `tesseract --version` (e.g. "tesseract 5.3.0")
//   - data_dir:        TESSDATA_PREFIX or compiled-in path
//   - langs:           list of installed language codes
//   - supported_psm:   "0..13" (inclusive ints accepted by --psm)
//   - supported_oem:   "0..3"  (inclusive ints accepted by --oem)
//
// PSM/OEM ranges are documented constants from upstream Tesseract and
// are stable across 4.x/5.x; we do not probe them at runtime.
func ProbeOCRTesseract() contract.BackendStatus {
	status := contract.BackendStatus{Name: "ocr_tesseract", Required: false}
	bin, err := exec.LookPath("tesseract")
	if err != nil {
		status.Ready = false
		status.Message = "tesseract not found on PATH; install via 'apt install tesseract-ocr tesseract-ocr-eng'"
		return status
	}
	status.Details = map[string]any{
		"binary":        bin,
		"supported_psm": "0..13",
		"supported_oem": "0..3",
	}
	if ver, ok := tesseractVersion(bin); ok {
		status.Details["version"] = ver
	}
	// Probe data-dir + langs via 'tesseract --list-langs'.
	out, runErr := exec.Command(bin, "--list-langs").CombinedOutput()
	if runErr != nil {
		status.Ready = true
		status.Message = "tesseract present but --list-langs failed"
		status.Details["error"] = runErr.Error()
		return status
	}
	langs, dataDir := parseListLangs(string(out))
	status.Ready = true
	status.Message = "available"
	if dataDir != "" {
		status.Details["data_dir"] = dataDir
	}
	if len(langs) > 0 {
		status.Details["langs"] = langs
	}
	status.Capabilities = langs
	return status
}

// tesseractVersion parses the first line of `tesseract --version`
// (e.g. "tesseract 5.3.0") and returns the trimmed string. Returns
// ok=false on any error.
func tesseractVersion(bin string) (string, bool) {
	out, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil && len(out) == 0 {
		return "", false
	}
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if line == "" {
		return "", false
	}
	return line, true
}

func parseListLangs(s string) (langs []string, dataDir string) {
	dataRe := regexp.MustCompile(`"([^"]+)"`)
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "List of available languages") {
			if m := dataRe.FindStringSubmatch(line); len(m) == 2 {
				dataDir = m[1]
			}
			continue
		}
		// Lang lines are bare tokens.
		if !strings.ContainsAny(line, " \t") {
			langs = append(langs, line)
		}
	}
	return langs, dataDir
}

// ProbeTemplateMatch reports which template-match backend is compiled
// in. The implementation is supplied by find_image_purego.go or
// find_image_gocv.go depending on build tags.
func ProbeTemplateMatch() contract.BackendStatus {
	return probeTemplateBackend()
}
