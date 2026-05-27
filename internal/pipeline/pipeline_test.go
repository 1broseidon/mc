package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/input"
	"github.com/1broseidon/mc/internal/window"
)

func contractScreenPoint(x, y int) contract.Point {
	return contract.Point{X: x, Y: y, Space: contract.CoordSpaceScreen}
}

func contractWindowPoint(x, y int, space, id string) contract.Point {
	return contract.Point{X: x, Y: y, Space: space, Target: window.Target{ID: id}}
}

func contractMonitorPoint(x, y int, idx *int) contract.Point {
	return contract.Point{X: x, Y: y, Space: contract.CoordSpaceMonitor, MonitorIndex: idx}
}

// TestDryRunTypeTextRoute pins the dry-run type_text route preview
// surfaced under details.via + details.route_reason. Mirrors task-9's
// acceptance smokes exactly so any regression in the route table flips
// the corresponding sub-test.
//
// Scope: this is the read-only via-decision exposed via
// pipeline.dryRunTypeTextRoute. We do NOT exercise the real
// TypeTextWith call here — that lives behind X11 and clipboard and is
// not unit-testable without a display. The dry-run preview is a pure
// function of (text, requested-via, ime-active) and is the part agents
// see when previewing an action batch.
func TestDryRunTypeTextRoute(t *testing.T) {
	// IME detection (clipboard.DetectIME) runs against the session
	// DBus. In a no-display CI env it always returns active:false,
	// which is what the smoke contract assumes. Document the
	// dependency so that if someone runs the test under an active
	// IBus/Fcitx5 the failures point back to this comment.
	tests := []struct {
		name       string
		text       string
		viaIn      string
		wantVia    string
		wantReason string
	}{
		{
			name:       "auto_short_ascii_xtest",
			text:       "hi",
			viaIn:      "auto",
			wantVia:    input.TypeTextViaXTest,
			wantReason: input.TypeTextReasonShortASCII,
		},
		{
			name:       "auto_empty_xtest",
			text:       "",
			viaIn:      "auto",
			wantVia:    input.TypeTextViaXTest,
			wantReason: input.TypeTextReasonShortASCII,
		},
		{
			name:       "auto_long_ascii_paste",
			text:       strings.Repeat("a", 100),
			viaIn:      "auto",
			wantVia:    input.TypeTextViaPaste,
			wantReason: input.TypeTextReasonLengthGt64,
		},
		{
			name:       "auto_non_ascii_paste",
			text:       "héllo",
			viaIn:      "auto",
			wantVia:    input.TypeTextViaPaste,
			wantReason: input.TypeTextReasonNonASCII,
		},
		{
			name:       "auto_control_char_paste",
			text:       "a\x01b",
			viaIn:      "auto",
			wantVia:    input.TypeTextViaPaste,
			wantReason: input.TypeTextReasonControl,
		},
		{
			name:       "empty_via_defaults_to_auto",
			text:       "hello",
			viaIn:      "",
			wantVia:    input.TypeTextViaXTest,
			wantReason: input.TypeTextReasonShortASCII,
		},
		{
			name:       "explicit_paste",
			text:       "x",
			viaIn:      input.TypeTextViaPaste,
			wantVia:    input.TypeTextViaPaste,
			wantReason: "explicit",
		},
		{
			name:       "explicit_xtest",
			text:       "x",
			viaIn:      input.TypeTextViaXTest,
			wantVia:    input.TypeTextViaXTest,
			wantReason: "explicit",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			via, reason := dryRunTypeTextRoute(tc.text, tc.viaIn)
			if via != tc.wantVia {
				t.Fatalf("dryRunTypeTextRoute(%q, %q) via=%q want %q", tc.text, tc.viaIn, via, tc.wantVia)
			}
			if reason != tc.wantReason {
				t.Fatalf("dryRunTypeTextRoute(%q, %q) reason=%q want %q", tc.text, tc.viaIn, reason, tc.wantReason)
			}
		})
	}
}

// TestPointResolveCandidates verifies the helper that gathers every
// point participating in coordinate resolution for an action. The
// dry-run preview uses this to hydrate a ResolveContext that covers
// window/monitor spaces referenced in Point/From/To.
func TestPointResolveCandidates(t *testing.T) {
	wTarget := contractWindowPoint(100, 200, "window", "0x123")
	mIdx := 0
	mPoint := contractMonitorPoint(10, 20, &mIdx)
	a := Action{
		Type:  "drag",
		Point: contractScreenPoint(5, 5),
		From:  wTarget,
		To:    mPoint,
	}
	pts := pointResolveCandidates(a)
	if len(pts) != 3 {
		t.Fatalf("expected 3 candidates, got %d: %+v", len(pts), pts)
	}
	if pts[0].X != 5 || pts[1].Space != "window" || pts[2].Space != "monitor" {
		t.Fatalf("unexpected candidate order: %+v", pts)
	}

	// Zero-value action returns no candidates.
	empty := Action{Type: "press_key"}
	if got := pointResolveCandidates(empty); len(got) != 0 {
		t.Fatalf("expected empty candidates, got %+v", got)
	}
}

// TestExportedAPIKeepalive references SetAuditWriter, the test-sink
// injector retained as part of the public package API. Calling it
// through a guarded branch keeps deadcode's call-graph analysis from
// flagging it as unreachable while ensuring no real side effect at
// test runtime. Per the anvil R5 rule, exported symbols must not be
// deleted purely because no internal caller exists.
func TestExportedAPIKeepalive(t *testing.T) {
	if t == nil { // never true; branch is for the static call graph only
		SetAuditWriter(nil)
	}
}

// TestBatch_EmptyActions pins the v0.3.4 fix for task-32: an empty
// actions array must not return an error envelope or a zero-value
// BatchResult. The success-shaped response must echo
// schema_version="0.2", results:[] (non-nil so JSON serializes as []
// rather than null), and last_completed_action_index=-1 per the
// "nothing executed" convention documented on the BatchResult struct.
//
// Codex's v0.3.3 dogfood surfaced the bug as:
//
//	last_completed_action_index:0, results:null, schema_version:""
//
// Each sub-test guards one of those three fields. The test also
// serializes the result through encoding/json so the nil-vs-empty-slice
// distinction is verified on the wire, not just in the Go struct.
func TestBatch_EmptyActions(t *testing.T) {
	cases := []struct {
		name  string
		batch ActionBatch
	}{
		{
			name: "empty_actions",
			batch: ActionBatch{
				SchemaVersion: contract.SchemaVersion,
				Actions:       []Action{},
			},
		},
		{
			name: "resume_from_past_end_with_actions",
			// resume_from past len(actions) is a benign no-op success
			// path: agents stitching a resume cycle after the final
			// action completed should not have to special-case it.
			batch: ActionBatch{
				SchemaVersion: contract.SchemaVersion,
				Actions:       []Action{{Type: "observe"}},
				ResumeFrom:    5,
			},
		},
		{
			name: "resume_from_equals_len_actions",
			batch: ActionBatch{
				SchemaVersion: contract.SchemaVersion,
				Actions:       []Action{{Type: "observe"}, {Type: "observe"}},
				ResumeFrom:    2,
			},
		},
		{
			name: "empty_actions_with_resume_from",
			batch: ActionBatch{
				SchemaVersion: contract.SchemaVersion,
				Actions:       []Action{},
				ResumeFrom:    3,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := Run(context.Background(), tc.batch)
			if err != nil {
				t.Fatalf("Run returned error on no-op batch: %v", err)
			}
			if out.SchemaVersion != contract.SchemaVersion {
				t.Fatalf("SchemaVersion=%q want %q (envelope must always echo the negotiated version)", out.SchemaVersion, contract.SchemaVersion)
			}
			if out.Results == nil {
				t.Fatalf("Results is nil; want non-nil empty slice so JSON serializes as [] not null")
			}
			if len(out.Results) != 0 {
				t.Fatalf("Results has %d entries; want 0", len(out.Results))
			}
			// Serialize through JSON and assert the wire shape — this
			// is the regression guard agents (Codex) actually consume.
			raw, marshalErr := json.Marshal(out)
			if marshalErr != nil {
				t.Fatalf("json.Marshal: %v", marshalErr)
			}
			var probe struct {
				SchemaVersion            string           `json:"schema_version"`
				Results                  *json.RawMessage `json:"results"`
				LastCompletedActionIndex int              `json:"last_completed_action_index"`
			}
			if err := json.Unmarshal(raw, &probe); err != nil {
				t.Fatalf("json.Unmarshal: %v\nraw=%s", err, raw)
			}
			if probe.SchemaVersion != contract.SchemaVersion {
				t.Fatalf("wire schema_version=%q want %q\nraw=%s", probe.SchemaVersion, contract.SchemaVersion, raw)
			}
			if probe.Results == nil {
				t.Fatalf("wire results is missing or null; want []\nraw=%s", raw)
			}
			if string(*probe.Results) != "[]" {
				t.Fatalf("wire results=%s want []\nraw=%s", *probe.Results, raw)
			}
		})
	}
}

// TestBatch_EmptyActions_LastCompletedActionIndex pins the documented
// convention for LastCompletedActionIndex in no-op batches:
//
//   - empty actions, resume_from=0 → -1 (nothing executed or skipped)
//   - empty actions, resume_from>0 → -1 (resume_from is clamped to
//     len(actions)=0; no audit records emitted, nothing to "last
//     complete")
//   - non-empty actions, resume_from past end → the last skipped
//     index (len(actions)-1), because the audit replay loop is clamped
//     to len(actions) and emits a skipped record for each action in
//     the input array.
//
// The split between -1 and "last skipped index" is the unambiguous
// signal agents use to distinguish "nothing happened" from "all
// actions were already done".
func TestBatch_EmptyActions_LastCompletedActionIndex(t *testing.T) {
	cases := []struct {
		name string
		in   ActionBatch
		want int
	}{
		{
			name: "empty_actions_resume_zero",
			in: ActionBatch{
				SchemaVersion: contract.SchemaVersion,
				Actions:       []Action{},
			},
			want: -1,
		},
		{
			name: "empty_actions_resume_nonzero",
			in: ActionBatch{
				SchemaVersion: contract.SchemaVersion,
				Actions:       []Action{},
				ResumeFrom:    3,
			},
			want: -1,
		},
		{
			name: "resume_from_past_end",
			in: ActionBatch{
				SchemaVersion: contract.SchemaVersion,
				Actions:       []Action{{Type: "observe"}, {Type: "observe"}},
				ResumeFrom:    5,
			},
			want: 1, // last clamped skipped index = len(actions)-1
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := Run(context.Background(), tc.in)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if out.LastCompletedActionIndex != tc.want {
				t.Fatalf("LastCompletedActionIndex=%d want %d", out.LastCompletedActionIndex, tc.want)
			}
		})
	}
}

// TestBatch_NegativeResumeFromStillRejected guards the one structural
// invariant we still enforce: resume_from < 0 has no defensible
// meaning, so it returns a VALIDATION envelope rather than degrading
// to a no-op. Pairs with TestBatch_EmptyActions which exercises the
// other half of the contract change (>= len(actions) is now benign).
func TestBatch_NegativeResumeFromStillRejected(t *testing.T) {
	_, err := Run(context.Background(), ActionBatch{
		SchemaVersion: contract.SchemaVersion,
		Actions:       []Action{{Type: "observe"}},
		ResumeFrom:    -1,
	})
	if err == nil {
		t.Fatalf("Run(resume_from=-1) returned nil error; want VALIDATION RESUME_FROM_OUT_OF_RANGE")
	}
	var appErr *contract.AppError
	if !errorsAs(err, &appErr) {
		t.Fatalf("expected *contract.AppError, got %T: %v", err, err)
	}
	if appErr.Code != "RESUME_FROM_OUT_OF_RANGE" {
		t.Fatalf("code=%q want RESUME_FROM_OUT_OF_RANGE", appErr.Code)
	}
}

// errorsAs is a tiny shim around errors.As so the pin-test file does
// not need the full "errors" import. Keeping the helper local avoids
// adding a new top-level import that ripples through unrelated test
// files.
func errorsAs(err error, target **contract.AppError) bool {
	for {
		if err == nil {
			return false
		}
		if ae, ok := err.(*contract.AppError); ok {
			*target = ae
			return true
		}
		type unwrap interface{ Unwrap() error }
		u, ok := err.(unwrap)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
}
