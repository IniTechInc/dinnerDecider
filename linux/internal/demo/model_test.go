package demo

import (
	"context"
	"testing"
	"time"

	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
	"github.com/IniTechInc/dinnerDecider/linux/internal/llm"
	"github.com/IniTechInc/dinnerDecider/linux/internal/scan"
)

// TestModelSatisfiesScanModel is the whole point of the package expressed as a
// compile-time assertion: the composition root swaps this in for
// *llm.Supervisor, and that swap has to keep working.
func TestModelSatisfiesScanModel(t *testing.T) {
	var _ scan.Model = (*Model)(nil)
}

func fast() *Model { return &Model{Latency: -1} }

// TestIdentifyItemParses proves the canned crop answer survives the real
// parser. If someone breaks fence stripping, this fails.
func TestIdentifyItemParses(t *testing.T) {
	m := fast()
	resp, err := m.Infer(context.Background(), llm.Request{Kind: llm.KindIdentifyItem})
	if err != nil {
		t.Fatalf("Infer: %v", err)
	}
	if resp.Kind != llm.KindIdentifyItem {
		t.Errorf("Kind = %q, want %q", resp.Kind, llm.KindIdentifyItem)
	}
	if resp.Item.Name != "Whole Milk" {
		t.Errorf("Item.Name = %q, want %q", resp.Item.Name, "Whole Milk")
	}
	if resp.Item.Brand == nil || *resp.Item.Brand != "Horizon" {
		t.Errorf("Item.Brand = %v, want Horizon", resp.Item.Brand)
	}
	if resp.Item.Confidence != 0.92 {
		t.Errorf("Item.Confidence = %v, want 0.92", resp.Item.Confidence)
	}
}

// TestAbsentBrandDecodesAsNil pins the three-way distinction the contract keeps
// between an absent brand, an explicit null and the empty string. "Large Eggs"
// is the second canned item and carries no brand.
func TestAbsentBrandDecodesAsNil(t *testing.T) {
	m := fast()
	ctx := context.Background()
	if _, err := m.Infer(ctx, llm.Request{Kind: llm.KindIdentifyItem}); err != nil {
		t.Fatalf("first Infer: %v", err)
	}
	resp, err := m.Infer(ctx, llm.Request{Kind: llm.KindIdentifyItem})
	if err != nil {
		t.Fatalf("second Infer: %v", err)
	}
	if resp.Item.Name != "Large Eggs" {
		t.Fatalf("Item.Name = %q, want %q", resp.Item.Name, "Large Eggs")
	}
	if resp.Item.Brand != nil {
		t.Errorf("Item.Brand = %q, want nil for an unbranded item", *resp.Item.Brand)
	}
}

// TestIdentifyAllItemsParses covers the whole-image pass, prose wrapper and all.
func TestIdentifyAllItemsParses(t *testing.T) {
	m := fast()
	resp, err := m.Infer(context.Background(), llm.Request{Kind: llm.KindIdentifyAllItems})
	if err != nil {
		t.Fatalf("Infer: %v", err)
	}
	if len(resp.Items) < 3 {
		t.Fatalf("len(Items) = %d, want at least 3", len(resp.Items))
	}
	for i, it := range resp.Items {
		if it.Name == "" {
			t.Errorf("Items[%d].Name is empty", i)
		}
		// Every canned item must clear the pipeline's noise cutoff, or a demo
		// scan would silently show an empty fridge.
		if it.Confidence < scan.NoiseConfidenceCutoff {
			t.Errorf("Items[%d] confidence %v is below the noise cutoff %v",
				i, it.Confidence, scan.NoiseConfidenceCutoff)
		}
	}
}

// TestEveryCannedItemRoundTrips walks the whole pool rather than the first
// entry, so a typo in any one of them fails here instead of on a demo run.
func TestEveryCannedItemRoundTrips(t *testing.T) {
	m := fast()
	ctx := context.Background()
	for i := range cannedItems {
		resp, err := m.Infer(ctx, llm.Request{Kind: llm.KindIdentifyItem})
		if err != nil {
			t.Fatalf("Infer %d: %v", i, err)
		}
		want := cannedItems[i]
		if resp.Item.Name != want.Name {
			t.Errorf("call %d: Name = %q, want %q", i, resp.Item.Name, want.Name)
		}
		if resp.Item.Category != want.Category {
			t.Errorf("call %d: Category = %q, want %q", i, resp.Item.Category, want.Category)
		}
	}
}

// TestRecipesParses covers the text-only bundle call.
func TestRecipesParses(t *testing.T) {
	m := fast()
	resp, err := m.Infer(context.Background(), llm.Request{Kind: llm.KindRecipes, Prompt: "anything"})
	if err != nil {
		t.Fatalf("Infer: %v", err)
	}
	if len(resp.Bundle.MakeNow) != 2 {
		t.Errorf("len(MakeNow) = %d, want 2", len(resp.Bundle.MakeNow))
	}
	if len(resp.Bundle.AlmostThere) != 1 {
		t.Errorf("len(AlmostThere) = %d, want 1", len(resp.Bundle.AlmostThere))
	}
	if len(resp.Bundle.AlmostThere) > 0 && len(resp.Bundle.AlmostThere[0].MissingItems) != 2 {
		t.Errorf("AlmostThere[0].MissingItems = %v, want 2 entries",
			resp.Bundle.AlmostThere[0].MissingItems)
	}
}

// TestUnknownKindErrors keeps the default branch honest.
func TestUnknownKindErrors(t *testing.T) {
	m := fast()
	if _, err := m.Infer(context.Background(), llm.Request{Kind: "nonsense"}); err == nil {
		t.Fatal("expected an error for an unknown request kind")
	}
}

// TestInferHonoursCancellation is the regression guard for the one place the
// demo path could diverge from the real one in a user-visible way: Cancel.
func TestInferHonoursCancellation(t *testing.T) {
	m := &Model{Latency: 10 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() {
		_, err := m.Infer(ctx, llm.Request{Kind: llm.KindIdentifyItem})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a cancellation error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Infer ignored a cancelled context and slept instead")
	}
}

// TestZeroLatencyBackendStillRefusesACancelledContext covers the branch where
// there is no timer to select on.
func TestZeroLatencyBackendStillRefusesACancelledContext(t *testing.T) {
	m := fast()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Infer(ctx, llm.Request{Kind: llm.KindIdentifyItem}); err == nil {
		t.Fatal("a zero-latency backend still must not serve a cancelled request")
	}
}

// TestEndSessionNeverClaimsToUnload guards the honesty of the status surface: a
// backend holding no memory must never report that it freed some.
func TestEndSessionNeverClaimsToUnload(t *testing.T) {
	m := fast()
	m.BeginSession()
	if got := m.EndSession(); got != contract.UnloadNothingToDo {
		t.Errorf("EndSession() = %v, want %v", got, contract.UnloadNothingToDo)
	}
	// Unbalanced EndSession must not underflow into a negative session count.
	if got := m.EndSession(); got != contract.UnloadNothingToDo {
		t.Errorf("unbalanced EndSession() = %v, want %v", got, contract.UnloadNothingToDo)
	}
}

// TestCallsCounts is what a status line reads to tell an operator the demo
// backend is the one answering.
func TestCallsCounts(t *testing.T) {
	m := fast()
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := m.Infer(ctx, llm.Request{Kind: llm.KindIdentifyItem}); err != nil {
			t.Fatalf("Infer %d: %v", i, err)
		}
	}
	if got := m.Calls(); got != 3 {
		t.Errorf("Calls() = %d, want 3", got)
	}
}
