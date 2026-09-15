package contract

// TerminalPhase decides how a FINISHED scan is reported to the user.
//
// Port of `ScanOutcome.terminalPhase(itemsFound:inferenceFailures:)` in
// DinnerDecider/Services/AppModel.swift, and of the rules in
// shared/pipeline/scan-pipeline.md § Stage 7 - terminal states:
//
//	itemsFound > 0        -> Finished   (regardless of how many calls failed)
//	inferenceFailures > 0 -> Failed
//	otherwise             -> Empty
//
// This is SHIPPED iOS behavior (commit b9268ef), not a Linux invention and not
// an open follow-up: "There IS behavior to copy, and a port must copy it."
//
// Why it is its own pure function: TestFlight feedback P2. Inference errors were
// swallowed (a `try?` on the whole-image pass, a bare `catch { print(...) }` in
// the crop loop) and the terminal phase was chosen purely on whether the item
// list was empty. So a scan where every single model call threw looked exactly
// like a scan of an empty fridge, and the UI told the user to add more light and
// get closer - blaming their photo for our failure. Empty keeps that photo
// advice because it is only honest when the model actually answered and saw
// nothing; Failed says the scan could not finish.
//
// A single swallowed failure is enough to make "Nothing spotted" a lie, so the
// Failed branch has no threshold: one is enough.
//
// Counting rule at the call site, stated here because getting it backwards
// defeats the function: only a THROWN inference counts as a failure. An
// identify call that returned an empty list from an unreadable response
// answered - it is not an inference failure - and BeginInference returning
// false never reached the model, so it is not one either.
//
// Two terminal paths exist in the engine and only this one consults this
// function. An ABORTED scan (the model was never ready and recovery is spent)
// sets Failed directly with a reason, exactly as the shipped Swift already
// exits before terminalPhase on the cancellation path. Callers must never
// inline "items empty ? Empty : Finished" - that is the bug this function
// replaced.
func TerminalPhase(itemsFound, inferenceFailures int) ScanPhase {
	// Anything found is worth showing, even if some calls failed alongside it.
	if itemsFound > 0 {
		return Finished
	}
	if inferenceFailures > 0 {
		return Failed
	}
	return Empty
}
