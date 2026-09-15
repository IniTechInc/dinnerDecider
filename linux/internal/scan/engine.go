// Package scan is the recognition pipeline: photos in, identified items out.
//
// engine.go is a transcription of `runScan` in
// DinnerDecider/Services/AppModel.swift, specified by
// shared/pipeline/scan-pipeline.md § Stage 1 - orchestration. Everything it
// needs already exists and is used rather than re-derived: the call budget, the
// dedupe, the prompts and the terminal-phase function live in internal/contract,
// the crops and the OCR in internal/vision, and the model gate, the inference
// bracket and the failure classifier in internal/llm.
//
// Three properties in here are field bugs in disguise if they are changed:
//
//  1. The session opens BEFORE the load and closes on EVERY exit path,
//     cancellation included. Closing the session is what runs a deferred
//     unload; skipping it leaves a multi-GB model resident forever.
//  2. The four-call budget is scan-wide across every photo, and the crops are
//     capped by budget.Remaining() BEFORE the loop consumes anything, so the
//     itemsTotal the user sees is what will actually be analysed.
//  3. Which outcomes count as inference failures, and which of the two terminal
//     paths a scan takes. See the tables on runScan and on infer.
package scan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"log"
	"strings"

	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
	"github.com/IniTechInc/dinnerDecider/linux/internal/llm"
	"github.com/IniTechInc/dinnerDecider/linux/internal/vision"
)

// Model is the engine's view of the llm supervisor.
//
// It is an interface for testability, but the set of methods is not a design
// choice: it is exactly the supervisor surface the scan is allowed to touch.
// Notably ABSENT is anything that talks to the lifecycle machine directly.
// Sessions, the capacity-1 inference gate and the BeginInference bracket all
// live behind Infer, because any path that reaches the machine without taking
// the gate reintroduces the concurrency bug the gate exists to close.
type Model interface {
	// BeginSession opens a user-visible model session. Sessions nest, so a
	// recipe generation may legitimately overlap a scan.
	BeginSession()
	// EndSession closes it and honours any unload deferred during it.
	EndSession() contract.UnloadDecision
	// EnsureLoaded loads the model if it is not resident.
	EnsureLoaded(ctx context.Context) error
	// Infer is the only admission point to the model: gate, then bracket.
	Infer(ctx context.Context, req llm.Request) (llm.Response, error)
	// Recover is the one-shot recovery, at most once per session.
	Recover(ctx context.Context, cause string) error
}

// The real supervisor must satisfy the interface the engine codes against. If
// this stops compiling, the scan and the runtime have drifted apart.
var _ Model = (*llm.Supervisor)(nil)

// Vision is the image work the scan does between the photo and the model.
type Vision interface {
	// Crops splits a photo into the regions Pass B analyses.
	Crops(img image.Image) vision.CropResult
	// ModelInput applies the tone curve and the 896 px downscale and encodes
	// the result for the wire.
	ModelInput(img image.Image) ([]byte, error)
}

// StdVision is the real Vision: internal/vision plus a JPEG encode.
type StdVision struct {
	// JPEGQuality defaults to ModelInputJPEGQuality.
	JPEGQuality int
}

// ModelInputJPEGQuality is the quality the proven wire protocol uses. It is 90
// because eval/run_eval.py `data_url` is 90, and that harness is the only
// measured evidence this port has about what the model sees.
const ModelInputJPEGQuality = 90

// Crops splits a photo into the regions Pass B analyses.
//
// On Linux there is no saliency detector, so this always returns the 16-tile
// grid with IsTileFallback = true - a path iOS takes routinely, not a degraded
// one. See vision.Crops.
func (v StdVision) Crops(img image.Image) vision.CropResult { return vision.Crops(img) }

// ModelInput runs the mandatory pre-inference preprocessing (tone curve, then
// the 896 px cap) and encodes the result as JPEG.
//
// The tone curve is unconditional and the downscale is not: see
// vision.DownscaleModelInput. On iOS this happens inside the LLM service, which
// is why a failure here is a failure of the CALL, counted like any other, and
// not a silent skip.
func (v StdVision) ModelInput(img image.Image) ([]byte, error) {
	if img == nil {
		return nil, errors.New("scan: no image to send to the model")
	}
	quality := v.JPEGQuality
	if quality <= 0 {
		quality = ModelInputJPEGQuality
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, vision.DownscaleModelInput(img), &jpeg.Options{Quality: quality}); err != nil {
		return nil, fmt.Errorf("scan: encoding the model input: %w", err)
	}
	return buf.Bytes(), nil
}

// Stage is the coarse step the scan is on, for the progress line.
type Stage string

const (
	// StageCropping is the per-photo preparation, including the whole-image
	// pass. iOS reports it before the crop split, and so does this.
	StageCropping Stage = "cropping"
	// StageIdentifying is the per-crop loop.
	StageIdentifying Stage = "identifying"
)

// Progress is the Scanning phase's payload, mirroring Swift's ScanProgress.
//
// ItemsTotal is the CAPPED crop count, never the raw one: a 16-tile photo with
// three calls left reports 3, because 3 is what will actually be analysed.
type Progress struct {
	PhotoIndex int   `json:"photoIndex"`
	PhotoCount int   `json:"photoCount"`
	ItemsDone  int   `json:"itemsDone"`
	ItemsTotal int   `json:"itemsTotal"`
	Stage      Stage `json:"stage"`
}

// Observer receives streaming updates. Every method may be called from the scan
// goroutine and must not block for long. A nil Observer is fine.
type Observer interface {
	// OnProgress reports a new progress line.
	OnProgress(Progress)
	// OnItems reports the deduped item list after it grew. The slice is freshly
	// allocated by each dedupe and is never mutated afterwards.
	OnItems([]contract.IdentifiedItem)
}

// Photo is one photo entering the scan, identified by UUID rather than by
// position: removal by a stale index is a no-op rather than a trap
// (scan-pipeline.md § Stage 0).
//
// Image must already be decoded, orientation-normalised and capture-downscaled
// (vision.DecodeNormalized then vision.DownscaleCapture). A photo with no
// usable pixel data is dropped before the scan starts, exactly as startScan
// filters `cgImage != nil || ciImage != nil`.
type Photo struct {
	ID    string
	Image image.Image
}

// Abort reasons. These are the only two, and both are set DIRECTLY without
// consulting TerminalPhase: see the Run doc comment.
const (
	// AbortReasonModelNotReady means the model was never ready and the one-shot
	// recovery is spent. The UI says the scan could not finish.
	AbortReasonModelNotReady = "model_not_ready"
	// AbortReasonInsufficientMemory means the recovery's MemAvailable preflight
	// refused to reload. The UI appends the memory detail, and on a Pi the
	// "close other apps" advice is literally correct.
	AbortReasonInsufficientMemory = "insufficient_memory"
)

// Result is everything a finished scan reports.
type Result struct {
	// Phase is one of Finished, Empty, Failed or Cancelled.
	Phase contract.ScanPhase
	// Items is the deduped item list, in first-seen order.
	Items []contract.IdentifiedItem
	// InferenceFailures is the count that drives TerminalPhase. It is 0 on an
	// abort: an abort never reached the model.
	InferenceFailures int
	// AbortReason is empty unless the scan aborted.
	AbortReason string
	// Err is the error behind an abort, for logs and for errors.Is.
	Err error
	// CallsUsed is how much of the four-call budget the scan spent.
	CallsUsed int
}

// AbortError marks the errors that end a scan rather than being counted and
// swallowed. It wraps the underlying error so errors.Is still sees
// llm.ErrModelNotReady or llm.ErrInsufficientMemory underneath.
type AbortError struct {
	Reason string
	Err    error
}

// Error implements error.
func (e *AbortError) Error() string {
	return fmt.Sprintf("scan aborted (%s): %v", e.Reason, e.Err)
}

// Unwrap exposes the underlying typed error.
func (e *AbortError) Unwrap() error { return e.Err }

// NoiseConfidenceCutoff is the low-confidence cutoff. Items at EXACTLY this
// value are KEPT: the comparison is strictly less-than
// (scan-pipeline.md § Stage 1).
const NoiseConfidenceCutoff = 0.15

// noiseName is the model's no-item escape hatch, compared case-insensitively.
const noiseName = "unknown"

// isNoise is the single filter, applied identically at all three call sites:
// the whole-image pass, the tile branch and the single-item branch.
//
//	isNoise(item) = lower(item.name) == "unknown" || item.confidence < 0.15
//
// Strictly less-than, so an item at exactly 0.15 is kept. The "unknown" half
// only works while the identify-item prompt keeps its escape-hatch sentence;
// the model (and the training data) use "unknown" at about 0.1 confidence for
// background-only crops.
func isNoise(item contract.IdentifiedItem) bool {
	return strings.ToLower(item.Name) == noiseName || item.Confidence < NoiseConfidenceCutoff
}

// Engine runs scans. The zero value is unusable (Model and Vision are
// required); everything else defaults.
type Engine struct {
	// Model is the supervisor.
	Model Model
	// Vision defaults to StdVision.
	Vision Vision
	// OCR defaults to vision.NoopOCR: an absent tesseract is a supported state
	// and the prompt boundary substitutes the literal `none`.
	OCR vision.OCR
	// CallBudget defaults to contract.DefaultScanCallLimit (4).
	//
	// A named, documented parameter, as scan-pipeline.md § ScanCallBudget
	// requires: a box without an 8 GB wired ceiling may legitimately want more.
	// The semantics never change - scan-wide, prefix-capping, stays exhausted -
	// and follow-up F2 blocks raising the DEFAULT until someone measures it on
	// hardware that exists.
	CallBudget int
	// Logf defaults to log.Printf. Per-crop failures are logged and never
	// silent: that silence is what made a systemic failure look like an empty
	// fridge.
	Logf func(format string, args ...any)
	// TerminalPhase defaults to contract.TerminalPhase and exists as a field so
	// a test can prove the ABORT path never consults it.
	TerminalPhase func(itemsFound, inferenceFailures int) contract.ScanPhase
}

func (e *Engine) vision() Vision {
	if e.Vision != nil {
		return e.Vision
	}
	return StdVision{}
}

func (e *Engine) ocr() vision.OCR {
	if e.OCR != nil {
		return e.OCR
	}
	return vision.NoopOCR{}
}

func (e *Engine) callBudget() int {
	if e.CallBudget > 0 {
		return e.CallBudget
	}
	return contract.DefaultScanCallLimit
}

func (e *Engine) logf(format string, args ...any) {
	if e.Logf != nil {
		e.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

func (e *Engine) terminalPhase(itemsFound, inferenceFailures int) contract.ScanPhase {
	if e.TerminalPhase != nil {
		return e.TerminalPhase(itemsFound, inferenceFailures)
	}
	return contract.TerminalPhase(itemsFound, inferenceFailures)
}

// Run executes one scan and returns its terminal state. It is the transcription
// of `runScan`.
//
// # The session bracket
//
//	BeginSession()        // BEFORE EnsureLoaded, so pressure during the load is covered
//	defer EndSession()    // EVERY exit path, cancellation included
//
// The whole scan is ONE model session, which is what stops a memory warning
// between two crop inferences from freeing the model mid-scan - the "9 items
// analyzed, nothing recognized" field bug. EndSession is deferred rather than
// called at the exits because closing the session is what runs a deferred
// unload: skipping it on the cancellation path leaves several GB wired forever.
//
// # Failure counting
//
//	Outcome                                   | Counted | Why
//	------------------------------------------+---------+----------------------
//	an inference threw or returned an error    | yes     | both swallow sites
//	                                           |         | count, and both log
//	IdentifyAllItems returned [] from a PARSE  | no      | it answered, we could
//	failure (no error at all)                  |         | not read it
//	llm.ErrModelNotReady                       | no      | never reached the
//	                                           |         | model - but never
//	                                           |         | silently skipped
//	llm.ErrInferenceQueueTimeout               | yes     | admitted, attempted,
//	                                           |         | gave up with nothing
//	llm.ErrCancelled                           | no      | cancellation has its
//	                                           |         | own terminal phase
//
// The classifier is llm.CountsAsInferenceFailure, used rather than re-derived:
// the supervisor produces these errors, so it owns what they mean. Getting the
// parse-failure row backwards manufactures a spurious Failed; getting the
// threw row backwards manufactures "Nothing spotted". Both shipped, in opposite
// directions.
//
// # Two terminal paths, and only one consults TerminalPhase
//
//   - NORMAL COMPLETION asks contract.TerminalPhase(len(items), failures).
//     Never inline "items empty ? Empty : Finished" - that is precisely the bug
//     the function exists to remove.
//   - An ABORT sets Failed directly with a reason, WITHOUT consulting
//     TerminalPhase. That is not a divergence: § Stage 7 governs how a FINISHED
//     scan is reported, and the shipped Swift already exits before
//     ScanOutcome.terminalPhase on the cancellation path. An aborted scan is the
//     same shape as a cancelled one.
//
// Cancellation takes the same early-exit shape and reports Cancelled, mirroring
// Swift's `if Task.isCancelled { return }` plus `cancelScan()`.
func (e *Engine) Run(ctx context.Context, photos []Photo, obs Observer) Result {
	// Opened BEFORE the load so pressure arriving during a multi-minute cold
	// load is already covered by the session.
	e.Model.BeginSession()
	defer e.Model.EndSession()

	if err := e.Model.EnsureLoaded(ctx); err != nil {
		// Swallowed exactly as Swift's `try? await llm.loadModel()` is. A load
		// that failed surfaces at the first call as ErrModelNotReady, which
		// runs the one-shot recovery, retries once, and aborts honestly if that
		// fails. It is never reported as an empty fridge.
		e.logf("[Scan] Model load failed, continuing so the first call can report honestly: %v", err)
	}

	// results is the RAW, pre-dedupe, scan-wide list. items is what the user
	// sees. len(items) - the deduped count - is what TerminalPhase is asked
	// about, exactly as Swift asks scannedItems.count.
	var results []contract.IdentifiedItem
	var items []contract.IdentifiedItem
	limit := e.callBudget()
	budget := contract.NewScanCallBudget(limit)
	inferenceFailures := 0

	// appendSurviving applies the single noise filter and re-dedupes, so the
	// UI streams a deduplicated list as the scan runs.
	appendSurviving := func(candidates ...contract.IdentifiedItem) {
		grew := false
		for _, item := range candidates {
			if isNoise(item) {
				continue
			}
			results = append(results, item)
			grew = true
		}
		if !grew {
			return
		}
		items = contract.Dedupe(results)
		if obs != nil {
			obs.OnItems(items)
		}
	}

	done := func(phase contract.ScanPhase, reason string, err error) Result {
		return Result{
			Phase:             phase,
			Items:             items,
			InferenceFailures: inferenceFailures,
			AbortReason:       reason,
			Err:               err,
			CallsUsed:         limit - budget.Remaining(),
		}
	}
	cancelled := func() Result {
		// Exits before TerminalPhase is ever consulted, as the Swift does.
		return done(contract.Cancelled, "", ctx.Err())
	}
	aborted := func(err error) Result {
		var abort *AbortError
		if !errors.As(err, &abort) {
			abort = &AbortError{Reason: AbortReasonModelNotReady, Err: err}
		}
		e.logf("[Scan] Aborting: %v", abort)
		// Failed, set DIRECTLY. TerminalPhase is not consulted, and the failure
		// count stays where it is: an abort never reached the model.
		return done(contract.Failed, abort.Reason, abort.Err)
	}

	for photoIndex, photo := range photos {
		if ctx.Err() != nil {
			return cancelled()
		}
		e.publish(obs, Progress{
			PhotoIndex: photoIndex,
			PhotoCount: len(photos),
			Stage:      StageCropping,
		})

		// --- Pass A: the whole image ---------------------------------------
		//
		// Asked BEFORE cropping, so items that fall between crops are still
		// seen. Pass A and Pass B are a UNION, not a choice: both append into
		// the same results list and dedupe reconciles them.
		if budget.Consume() {
			// A photo with no usable pixels is skipped without counting, which
			// is what the Swift's `let cgImage = ...` guard does. The manager
			// drops these before the scan starts, so it is belt and braces.
			if photo.Image == nil {
				e.logf("[Scan] Photo %d carries no usable pixel data, skipping the whole-image pass", photoIndex+1)
			} else {
				resp, err := e.infer(ctx, photo.Image, llm.KindIdentifyAllItems)
				switch {
				case isAbort(err):
					return aborted(err)
				case err != nil:
					e.logf("[Scan] Whole-image pass failed: %v", err)
					if llm.CountsAsInferenceFailure(err) {
						inferenceFailures++
					}
				default:
					// resp.Items may legitimately be empty: an unreadable
					// response answered, and answering unreadably is not an
					// inference failure.
					appendSurviving(resp.Items...)
				}
			}
		}

		if ctx.Err() != nil {
			return cancelled()
		}

		// --- Pass B: the crops ---------------------------------------------
		cropResult := e.vision().Crops(photo.Image)
		// Capped by what the budget has left BEFORE the loop consumes any of
		// it, so itemsTotal is what will actually be analysed rather than the
		// raw crop count. A 16-tile photo after Pass A yields exactly 3.
		cappedCrops := cropResult.Images
		if remaining := budget.Remaining(); len(cappedCrops) > remaining {
			cappedCrops = cappedCrops[:remaining]
		}
		total := len(cappedCrops)

		for index, crop := range cappedCrops {
			if !budget.Consume() {
				break
			}
			if ctx.Err() != nil {
				return cancelled()
			}
			e.publish(obs, Progress{
				PhotoIndex: photoIndex,
				PhotoCount: len(photos),
				ItemsDone:  index,
				ItemsTotal: total,
				Stage:      StageIdentifying,
			})

			// IsTileFallback chooses the PROMPT: a tile is a large region that
			// may hold several things, so it gets identify-all-items; a
			// detected box is one object, so it gets identify-item.
			kind := llm.KindIdentifyItem
			if cropResult.IsTileFallback {
				kind = llm.KindIdentifyAllItems
			}
			resp, err := e.infer(ctx, crop, kind)
			if isAbort(err) {
				return aborted(err)
			}
			if err != nil {
				// Logged, counted, and NEVER aborting the scan.
				e.logf("[Scan] Crop %d/%d failed: %v", index+1, total, err)
				if llm.CountsAsInferenceFailure(err) {
					inferenceFailures++
				}
				continue
			}
			if kind == llm.KindIdentifyAllItems {
				appendSurviving(resp.Items...)
			} else {
				appendSurviving(resp.Item)
			}
		}
	}

	if ctx.Err() != nil {
		return cancelled()
	}
	// The ONE call to the contract function, on the normal-completion path.
	return done(e.terminalPhase(len(items), inferenceFailures), "", nil)
}

// Recipes is the recipe generation, and it goes through the same gate and the
// same bracket as every scan call.
//
// It is here, in the scan package, precisely so that it cannot quietly grow its
// own path to the model: model-lifecycle.md § Adapter obligations, obligation 4,
// is "bracket every single model call - INCLUDING the recipe generation, not
// just the scan."
//
// It opens its own session. Sessions nest, so this may overlap a running scan;
// the deferred unload fires only when the last one ends. It is deliberately NOT
// refused while a scan runs (plan §0.1): it is one short text-only call and it
// simply queues on the gate, which is the case the gate exists for.
//
// It does not touch the scan call budget: the budget bounds the HEAVY image
// calls of one scan.
func (e *Engine) Recipes(ctx context.Context, prompt string) (contract.RecipeBundleResponse, error) {
	e.Model.BeginSession()
	defer e.Model.EndSession()

	if err := e.Model.EnsureLoaded(ctx); err != nil {
		e.logf("[Recipes] Model load failed, continuing so the call can report honestly: %v", err)
	}
	resp, err := e.inferRequest(ctx, llm.Request{Kind: llm.KindRecipes, Prompt: prompt})
	if err != nil {
		return contract.RecipeBundleResponse{}, err
	}
	return resp.Bundle, nil
}

// publish sends a progress line to the observer, if there is one.
func (e *Engine) publish(obs Observer, p Progress) {
	if obs != nil {
		obs.OnProgress(p)
	}
}

// infer prepares one image and makes one model call.
//
// The OCR runs on the crop at its native size, immediately before the call, and
// NEVER fails a scan: an OCR problem yields an empty string and the prompt
// boundary substitutes the literal `none`.
//
// The preprocessing (tone curve, 896 px cap, encode) sits INSIDE the call for a
// reason: on iOS it lives inside the LLM service, inside the try. So a
// preparation failure is a failure of this call site and is counted like any
// other, rather than being a silent skip that ends at "Nothing spotted".
func (e *Engine) infer(ctx context.Context, img image.Image, kind llm.RequestKind) (llm.Response, error) {
	ocrText := e.ocr().RecognizeText(img)
	prompt := contract.IdentifyAllItemsPrompt(ocrText)
	if kind == llm.KindIdentifyItem {
		prompt = contract.IdentifyItemPrompt(ocrText)
	}
	jpegBytes, err := e.vision().ModelInput(img)
	if err != nil {
		return llm.Response{}, err
	}
	return e.inferRequest(ctx, llm.Request{Kind: kind, Prompt: prompt, ImageJPEG: jpegBytes})
}

// inferRequest makes one model call and handles llm.ErrModelNotReady.
//
// ErrModelNotReady means the call never reached the model: with the gate held,
// the only remaining cause is that the child died between gate acquisition and
// bracket open. It is NOT counted as an inference failure - counting a
// non-answer that never touched the model manufactures a spurious Failed.
//
// It is not silently skipped either. Skip-and-continue for four calls ends at
// TerminalPhase(0, 0) = Empty, which is the "Nothing spotted" lie the whole
// ScanOutcome split exists to kill. Instead: run the supervisor's one-shot
// recovery, retry the SAME call once, and abort only if that fails.
//
// At most one recovery happens per session - the supervisor enforces that, and
// returns ErrRecoveryExhausted once it is spent. A cold reload on a Pi costs
// minutes, and burning the user's time twice in silence is worse than an honest
// error.
func (e *Engine) inferRequest(ctx context.Context, req llm.Request) (llm.Response, error) {
	resp, err := e.Model.Infer(ctx, req)
	if err == nil || !errors.Is(err, llm.ErrModelNotReady) {
		return resp, err
	}

	e.logf("[Scan] The model was not ready for a %s call; attempting the one-shot recovery: %v", req.Kind, err)
	if rerr := e.Model.Recover(ctx, "scan: "+string(req.Kind)+" found the model not ready"); rerr != nil {
		if errors.Is(rerr, llm.ErrCancelled) || errors.Is(rerr, context.Canceled) || ctx.Err() != nil {
			// Not an abort: cancellation has its own terminal phase.
			return llm.Response{}, llm.ErrCancelled
		}
		return llm.Response{}, &AbortError{Reason: abortReasonFor(rerr), Err: rerr}
	}

	// The retry is the SAME call, once.
	resp, err = e.Model.Infer(ctx, req)
	if err != nil && errors.Is(err, llm.ErrModelNotReady) {
		return llm.Response{}, &AbortError{Reason: AbortReasonModelNotReady, Err: err}
	}
	return resp, err
}

// abortReasonFor maps a failed recovery onto the abort reason the UI explains.
//
// A recovery blocked by the MemAvailable preflight is its own reason: the box
// is out of memory, the model stays unloaded so it can recover, and telling the
// user to close other apps is literally correct on a Pi.
func abortReasonFor(err error) string {
	if errors.Is(err, llm.ErrInsufficientMemory) {
		return AbortReasonInsufficientMemory
	}
	return AbortReasonModelNotReady
}

// isAbort reports whether an error ends the scan rather than being counted.
func isAbort(err error) bool {
	var abort *AbortError
	return errors.As(err, &abort)
}
