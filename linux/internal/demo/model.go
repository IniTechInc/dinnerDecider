// Package demo is the inference backend used when the real one is unavailable.
//
// The HTTP surface already treats this as a first-class state rather than an
// error: api.Router.demoMode reports "scans return sample data instead of real
// recognition" for a service with no weights, no mmproj, no inference backend,
// or an architecture with no official llama-server build. Something has to
// actually produce that sample data, and this is it.
//
// It is the port of iOS's MockLLMService, and it is deliberately NOT a stub
// that returns pre-decoded structs. It emits the same shape of raw completion
// text a real model emits -- prose wrapped around a fenced JSON block -- and
// runs it back through contract.Decode*, which is the same parser the real
// client uses. A demo scan therefore exercises the fence stripping and the
// decode path for real, so a regression in the parser fails a demo run instead
// of hiding until someone has weights.
package demo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
	"github.com/IniTechInc/dinnerDecider/linux/internal/llm"
)

// DefaultLatency is the simulated per-call inference latency.
//
// Not zero, on purpose. The scan UI streams progress over SSE and the whole
// point of the demo mode is to exercise that: an instant backend produces a
// scan that finishes before the browser has subscribed, which demonstrates
// nothing and hides ordering bugs. It is still orders of magnitude below the
// minutes a Pi takes, so a demo run stays visibly a demo run.
const DefaultLatency = 300 * time.Millisecond

// cannedItem is one entry of the sample fridge.
type cannedItem struct {
	Name       string
	Brand      string
	Category   string
	Confidence float64
}

// cannedItems is the sample inventory, kept in step with iOS's MockLLMService
// so a demo scan on a Pi and a demo scan in the simulator show the same fridge.
var cannedItems = []cannedItem{
	{"Whole Milk", "Horizon", "dairy", 0.92},
	{"Large Eggs", "", "dairy", 0.88},
	{"Cheddar Cheese", "Tillamook", "dairy", 0.90},
	{"Roma Tomatoes", "", "produce", 0.85},
	{"Baby Spinach", "Earthbound", "produce", 0.83},
	{"Chicken Breast", "", "meat", 0.87},
	{"Spaghetti", "Barilla", "pantry", 0.94},
	{"Black Beans", "Bush's", "pantry", 0.90},
	{"Olive Oil", "California Olive Ranch", "condiment", 0.89},
	{"Greek Yogurt", "Fage", "dairy", 0.86},
}

// Model is the demo backend. It satisfies scan.Model, so the composition root
// substitutes it for *llm.Supervisor with no other change anywhere.
//
// The zero value is usable.
type Model struct {
	// Latency is the simulated per-call cost. Zero means DefaultLatency; a
	// negative value means none at all, which is what the tests want.
	Latency time.Duration
	// Logf is optional.
	Logf func(format string, args ...any)

	mu       sync.Mutex
	calls    int
	sessions int
}

// BeginSession records the session bracket the scan engine opens around a whole
// scan. Sessions nest, exactly as they do on the supervisor.
func (m *Model) BeginSession() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions++
}

// EndSession closes the bracket. There is never anything resident to unload, so
// the honest answer is always UnloadNothingToDo -- the demo backend must not
// report freeing memory it never took.
func (m *Model) EndSession() contract.UnloadDecision {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions > 0 {
		m.sessions--
	}
	return contract.UnloadNothingToDo
}

// EnsureLoaded succeeds immediately. There is no model to load.
func (m *Model) EnsureLoaded(context.Context) error { return nil }

// Recover succeeds: there is no child process to restart, so a recovery request
// is trivially satisfied rather than an error.
func (m *Model) Recover(_ context.Context, cause string) error {
	if m.Logf != nil {
		m.Logf("[demo] recovery requested (%s); nothing to recover", cause)
	}
	return nil
}

// ErrUnknownKind is returned for a request kind this backend does not serve.
var ErrUnknownKind = errors.New("demo: unknown request kind")

// Infer answers one call with canned text, decoded by the real parser.
//
// It honours cancellation while it sleeps: a cancelled scan must abort here the
// same way it would abort mid-inference against a real model, or the demo path
// would be the only one where Cancel appears not to work.
func (m *Model) Infer(ctx context.Context, req llm.Request) (llm.Response, error) {
	started := time.Now()
	if err := m.sleep(ctx); err != nil {
		return llm.Response{}, err
	}

	call := m.nextCall()

	switch req.Kind {
	case llm.KindIdentifyItem:
		text := itemCompletion(call)
		item, ok := contract.DecodeIdentifiedItem(text)
		if !ok {
			// Unreachable unless the canned text or the parser breaks, which is
			// exactly the regression this backend is shaped to catch.
			return llm.Response{}, fmt.Errorf("demo: canned identify_item text failed to parse")
		}
		return llm.Response{
			Kind:    req.Kind,
			Text:    text,
			Item:    item,
			Elapsed: time.Since(started),
		}, nil

	case llm.KindIdentifyAllItems:
		text := allItemsCompletion(call)
		items, ok := contract.DecodeIdentifiedItemArray(text)
		if !ok {
			return llm.Response{}, fmt.Errorf("demo: canned identify_all_items text failed to parse")
		}
		return llm.Response{
			Kind:    req.Kind,
			Text:    text,
			Items:   items,
			Elapsed: time.Since(started),
		}, nil

	case llm.KindRecipes:
		text := recipesCompletion()
		bundle, ok := contract.DecodeRecipeBundle(text)
		if !ok {
			return llm.Response{}, fmt.Errorf("demo: canned recipes text failed to parse")
		}
		return llm.Response{
			Kind:    req.Kind,
			Text:    text,
			Bundle:  bundle,
			Elapsed: time.Since(started),
		}, nil

	default:
		return llm.Response{}, fmt.Errorf("%w %q", ErrUnknownKind, req.Kind)
	}
}

// Calls reports how many inferences have been served, for the tests and for an
// operator wondering whether the demo backend is the one answering.
func (m *Model) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func (m *Model) nextCall() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := m.calls
	m.calls++
	return n
}

func (m *Model) sleep(ctx context.Context) error {
	d := m.Latency
	if d == 0 {
		d = DefaultLatency
	}
	if d <= 0 {
		// Still honour an already-cancelled context, so a cancelled scan does
		// not get one free inference on a zero-latency backend.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// itemCompletion is one crop's answer, as a bare fenced object.
func itemCompletion(call int) string {
	it := cannedItems[call%len(cannedItems)]
	return "```json\n" + itemJSON(it) + "\n```"
}

// allItemsCompletion is the whole-image pass: three to five items, wrapped in
// the prose a real model volunteers whether or not it was asked to.
func allItemsCompletion(call int) string {
	count := 3 + call%3
	if count > len(cannedItems) {
		count = len(cannedItems)
	}
	start := call % len(cannedItems)

	objects := make([]string, 0, count)
	for i := 0; i < count; i++ {
		objects = append(objects, itemJSON(cannedItems[(start+i)%len(cannedItems)]))
	}

	return "Here is what I can see in the photo:\n\n```json\n[\n" +
		strings.Join(objects, ",\n") +
		"\n]\n```\n\nThat is everything I could identify."
}

// itemJSON renders one item. An empty brand becomes an explicit null rather
// than an empty string: the contract keeps those distinguishable and the
// conformance fixtures pin it.
func itemJSON(it cannedItem) string {
	brand := "null"
	if it.Brand != "" {
		brand = fmt.Sprintf("%q", it.Brand)
	}
	return fmt.Sprintf(`  {"name": %q, "brand": %s, "category": %q, "confidence": %g}`,
		it.Name, brand, it.Category, it.Confidence)
}

// recipesCompletion mirrors the iOS mock's bundle, prose and fence included.
func recipesCompletion() string {
	return "Sure! Based on what you have, here are some ideas:\n\n```json\n" +
		recipeBundleJSON + "\n```\n\nEnjoy your dinner!"
}

const recipeBundleJSON = `{
  "makeNow": [
    {
      "name": "Cheesy Scrambled Eggs",
      "ingredients": [
        {"name": "Large Eggs", "hasIt": true},
        {"name": "Cheddar Cheese", "hasIt": true},
        {"name": "Whole Milk", "hasIt": true}
      ],
      "steps": [
        "Whisk the eggs with a splash of milk.",
        "Cook gently in a nonstick pan over low heat.",
        "Fold in grated cheddar just before serving."
      ],
      "timeMinutes": 10,
      "missingItems": []
    },
    {
      "name": "Simple Spaghetti Pomodoro",
      "ingredients": [
        {"name": "Spaghetti", "hasIt": true},
        {"name": "Roma Tomatoes", "hasIt": true},
        {"name": "Olive Oil", "hasIt": true}
      ],
      "steps": [
        "Boil the spaghetti until al dente.",
        "Simmer chopped tomatoes in olive oil.",
        "Toss the pasta in the sauce and serve."
      ],
      "timeMinutes": 20,
      "missingItems": []
    }
  ],
  "almostThere": [
    {
      "name": "Weeknight Black Bean Chili",
      "ingredients": [
        {"name": "Black Beans", "hasIt": true},
        {"name": "Roma Tomatoes", "hasIt": true},
        {"name": "Onion", "hasIt": false},
        {"name": "Chili Powder", "hasIt": false}
      ],
      "steps": [
        "Saute the onion until soft.",
        "Add tomatoes, beans, and chili powder.",
        "Simmer for 20 minutes and serve."
      ],
      "timeMinutes": 30,
      "missingItems": ["Onion", "Chili Powder"]
    }
  ]
}`
