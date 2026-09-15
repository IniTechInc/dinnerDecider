package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
)

// captureServer records the last request body and replies with a canned
// completion.
type captureServer struct {
	*httptest.Server
	lastBody   map[string]any
	lastRaw    []byte
	lastAuth   string
	lastPath   string
	status     int
	reply      string
	rawReply   string
	forceDelay time.Duration
}

func newCaptureServer(t *testing.T, reply string) *captureServer {
	t.Helper()
	cs := &captureServer{status: http.StatusOK, reply: reply}
	cs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.lastPath = r.URL.Path
		cs.lastAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		cs.lastRaw = raw
		_ = json.Unmarshal(raw, &cs.lastBody)
		if cs.forceDelay > 0 {
			time.Sleep(cs.forceDelay)
		}
		if r.URL.Path == HealthPath {
			w.WriteHeader(cs.status)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(cs.status)
		if cs.rawReply != "" {
			_, _ = w.Write([]byte(cs.rawReply))
			return
		}
		payload := map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]any{"content": cs.reply},
				"finish_reason": "stop",
			}},
			"usage":   map[string]any{"prompt_tokens": 312, "completion_tokens": 48},
			"timings": map[string]any{"predicted_per_second": 10.3},
		}
		_ = json.NewEncoder(w).Encode(payload)
	}))
	t.Cleanup(cs.Close)
	return cs
}

func (cs *captureServer) client() *Client { return NewClient(cs.URL, "test-key", 5*time.Second) }

// TestRequestShapeMirrorsTheEvalHarness pins the wire shape against
// eval/run_eval.py (LlamaClient.chat, image_part): one user message whose
// content is a parts array, image first then text, with the image as a base64
// data URL.
func TestRequestShapeMirrorsTheEvalHarness(t *testing.T) {
	cs := newCaptureServer(t, `{"name":"Whole Milk","brand":"Horizon","category":"dairy","confidence":0.82}`)
	c := cs.client()

	if _, err := c.IdentifyItem(context.Background(), contract.IdentifyItemPrompt(""), []byte{0xFF, 0xD8, 0xFF}); err != nil {
		t.Fatalf("IdentifyItem: %v", err)
	}

	if cs.lastPath != ChatPath {
		t.Fatalf("path = %q, want %q", cs.lastPath, ChatPath)
	}
	if cs.lastAuth != "Bearer test-key" {
		t.Fatalf("authorization = %q", cs.lastAuth)
	}
	if got := cs.lastBody["model"]; got != DefaultModelAlias {
		t.Fatalf("model = %v, want %q", got, DefaultModelAlias)
	}

	messages, ok := cs.lastBody["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("messages = %v, want exactly one", cs.lastBody["messages"])
	}
	message := messages[0].(map[string]any)
	if message["role"] != "user" {
		t.Fatalf("role = %v, want user", message["role"])
	}
	parts, ok := message["content"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("content = %v, want a two-part array", message["content"])
	}
	image := parts[0].(map[string]any)
	if image["type"] != "image_url" {
		t.Fatalf("first part type = %v, want image_url (eval sends the image first)", image["type"])
	}
	url := image["image_url"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(url, "data:image/jpeg;base64,") {
		t.Fatalf("image url = %q, want a jpeg data URL", url)
	}
	text := parts[1].(map[string]any)
	if text["type"] != "text" {
		t.Fatalf("second part type = %v, want text", text["type"])
	}
	if text["text"] != contract.IdentifyItemPrompt("") {
		t.Fatalf("prompt was not sent verbatim")
	}
}

// TestBodyOmitsResponseFormatAndKeepsSamplingParams is the carve-out test.
//
// Both halves would be invisible in production. A response_format copied over
// from the eval harness poisons calls 2 to 4 of every scan and reads as
// "Nothing spotted"; dropped top_k/top_p silently change the sampling that every
// recall number in this project was measured with. Neither ever surfaces as an
// error, so they are asserted on the wire.
func TestBodyOmitsResponseFormatAndKeepsSamplingParams(t *testing.T) {
	cs := newCaptureServer(t, `[]`)
	c := cs.client()
	if _, err := c.IdentifyAllItems(context.Background(), contract.IdentifyAllItemsPrompt("none"), []byte{1, 2, 3}); err != nil {
		t.Fatalf("IdentifyAllItems: %v", err)
	}

	for _, banned := range []string{"response_format", "json_schema", "grammar", "gbnf", "max_tokens", "stop"} {
		if _, present := cs.lastBody[banned]; present {
			t.Errorf("request body contains %q; see response-parsing.md § PROHIBITION", banned)
		}
		if strings.Contains(string(cs.lastRaw), `"`+banned+`"`) {
			t.Errorf("raw request body mentions %q", banned)
		}
	}

	if got := cs.lastBody["temperature"]; got != SamplingTemperature {
		t.Errorf("temperature = %v, want %v", got, SamplingTemperature)
	}
	topK, ok := cs.lastBody["top_k"]
	if !ok {
		t.Fatal("top_k is missing from the request body")
	}
	if topK != float64(SamplingTopK) {
		t.Errorf("top_k = %v, want %v", topK, SamplingTopK)
	}
	topP, ok := cs.lastBody["top_p"]
	if !ok {
		t.Fatal("top_p is missing from the request body")
	}
	if topP != SamplingTopP {
		t.Errorf("top_p = %v, want %v", topP, SamplingTopP)
	}
}

// TestCachePromptIsSentExplicitlyFalse guards a Go-specific trap: bool false is
// the zero value, so an `omitempty` tag on CachePrompt would drop the key
// entirely and leave prompt caching at whatever the server defaults to. This is
// the llama-server equivalent of iOS's resetSession() between requests.
func TestCachePromptIsSentExplicitlyFalse(t *testing.T) {
	cs := newCaptureServer(t, `[]`)
	if _, err := cs.client().IdentifyAllItems(context.Background(), "p", []byte{1}); err != nil {
		t.Fatalf("IdentifyAllItems: %v", err)
	}
	value, present := cs.lastBody["cache_prompt"]
	if !present {
		t.Fatal("cache_prompt is missing; an omitempty tag would do exactly this")
	}
	if value != false {
		t.Fatalf("cache_prompt = %v, want false", value)
	}
	if !strings.Contains(string(cs.lastRaw), `"cache_prompt":false`) {
		t.Fatalf("raw body does not carry cache_prompt:false: %s", cs.lastRaw)
	}
}

// TestIdentifyItemThrowsOnParseFailure and TestIdentifyAllItemsSwallowsIt are
// the two halves of the contractual asymmetry that drives the scan engine's
// failure counting.
func TestIdentifyItemThrowsOnParseFailure(t *testing.T) {
	cs := newCaptureServer(t, "I think that might be some sort of milk?")
	_, err := cs.client().IdentifyItem(context.Background(), "p", []byte{1})
	if !errors.Is(err, ErrBadResponse) {
		t.Fatalf("err = %v, want ErrBadResponse", err)
	}
}

func TestIdentifyAllItemsSwallowsParseFailure(t *testing.T) {
	cs := newCaptureServer(t, "Sorry, I cannot help with that.")
	items, err := cs.client().IdentifyAllItems(context.Background(), "p", []byte{1})
	if err != nil {
		t.Fatalf("IdentifyAllItems must return no error at all on a parse failure, got %v", err)
	}
	if items == nil {
		t.Fatal("the empty result must be a non-nil empty slice: nil is reserved for 'not a result'")
	}
	if len(items) != 0 {
		t.Fatalf("items = %v, want empty", items)
	}
}

// TestIdentifyAllItemsStillErrorsOnTransportFailure separates the two cases: an
// unreadable answer is not an inference failure, but a call that never answered
// is.
func TestIdentifyAllItemsStillErrorsOnTransportFailure(t *testing.T) {
	cs := newCaptureServer(t, "")
	cs.status = http.StatusInternalServerError
	cs.rawReply = `{"error":{"message":"context shift is disabled"}}`
	if _, err := cs.client().IdentifyAllItems(context.Background(), "p", []byte{1}); err == nil {
		t.Fatal("a 500 must be an error even though a parse failure is not")
	}
}

// TestParserSalvageRunsOnTheWireFormat proves the transport hands the raw text
// to the contract parser untouched: the fenced, channel-prefixed shape is what
// this GGUF actually emits without a grammar, and salvaging it is the parser's
// job, not the client's.
func TestParserSalvageRunsOnTheWireFormat(t *testing.T) {
	reply := "analysis<channel|>```json\n[{\"name\":\"Eggs\",\"brand\":null,\"category\":\"dairy\",\"confidence\":0.71}]\n```"
	cs := newCaptureServer(t, reply)
	items, err := cs.client().IdentifyAllItems(context.Background(), "p", []byte{1})
	if err != nil {
		t.Fatalf("IdentifyAllItems: %v", err)
	}
	if len(items) != 1 || items[0].Name != "Eggs" {
		t.Fatalf("items = %+v", items)
	}
	if items[0].Brand != nil {
		t.Fatal("an explicit null brand must decode to nil")
	}
}

// TestRecipesUseATextOnlyMessage: the recipe call carries no image, so it must
// not send an image part at all.
func TestRecipesUseATextOnlyMessage(t *testing.T) {
	cs := newCaptureServer(t, `{"makeNow":[],"almostThere":[]}`)
	if _, err := cs.client().GenerateRecipes(context.Background(), "prompt"); err != nil {
		t.Fatalf("GenerateRecipes: %v", err)
	}
	parts := cs.lastBody["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if len(parts) != 1 {
		t.Fatalf("content = %v, want one text part", parts)
	}
	if parts[0].(map[string]any)["type"] != "text" {
		t.Fatalf("part type = %v, want text", parts[0])
	}
}

func TestRecipesThrowOnParseFailure(t *testing.T) {
	cs := newCaptureServer(t, "here are some ideas: pasta, soup")
	_, err := cs.client().GenerateRecipes(context.Background(), "prompt")
	if !errors.Is(err, ErrBadResponse) {
		t.Fatalf("err = %v, want ErrBadResponse", err)
	}
}

// TestEmptyChoicesIsAnErrorNotAnEmptyAnswer pins the shape a wedged grammar
// sampler produced on iOS. Reporting it as "the model saw nothing" is the exact
// lie the ScanOutcome split exists to kill.
func TestEmptyChoicesIsAnErrorNotAnEmptyAnswer(t *testing.T) {
	cs := newCaptureServer(t, "")
	cs.rawReply = `{"choices":[]}`
	_, err := cs.client().IdentifyAllItems(context.Background(), "p", []byte{1})
	if err == nil {
		t.Fatal("zero choices must be an error")
	}
	if errors.Is(err, ErrBadResponse) {
		t.Fatal("zero choices is a failed call, not an unreadable answer")
	}
}

// TestCompletionCarriesTheBenchNumbers: step 77 reports per-call tok/s from
// llama-server's timings block, so the transport must not drop it.
func TestCompletionCarriesTheBenchNumbers(t *testing.T) {
	cs := newCaptureServer(t, "hello")
	got, err := cs.client().Chat(context.Background(), []Part{TextPart("hi")})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got.Text != "hello" || got.FinishReason != "stop" {
		t.Fatalf("completion = %+v", got)
	}
	if got.PromptTokens != 312 || got.PredictedTokens != 48 {
		t.Fatalf("usage not carried: %+v", got)
	}
	if got.PredictedPerSecond != 10.3 {
		t.Fatalf("timings not carried: %+v", got)
	}
}

// TestContextCancellationPropagates: the gate and the scan both cancel through
// the context, so the transport has to honour it.
func TestContextCancellationPropagates(t *testing.T) {
	cs := newCaptureServer(t, "hello")
	cs.forceDelay = 2 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := cs.client().Chat(ctx, []Part{TextPart("hi")})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// TestHealthReadsTheStartupSignal covers both shapes: 200 means serving, and the
// 503 llama-server returns while the model is still loading is the whole startup
// poll.
func TestHealthReadsTheStartupSignal(t *testing.T) {
	cs := newCaptureServer(t, "")
	c := cs.client()
	if err := c.Health(context.Background()); err != nil {
		t.Fatalf("healthy server: %v", err)
	}
	cs.status = http.StatusServiceUnavailable
	if err := c.Health(context.Background()); err == nil {
		t.Fatal("a 503 must not read as healthy")
	}
}

// TestJPEGDataURLMatchesEvalEncoding keeps the encoding identical to
// eval/run_eval.py's data_url: standard base64, no wrapping, jpeg media type.
func TestJPEGDataURLMatchesEvalEncoding(t *testing.T) {
	got := JPEGDataURL([]byte{0xFF, 0xD8, 0xFF, 0xE0})
	const want = "data:image/jpeg;base64,/9j/4A=="
	if got != want {
		t.Fatalf("JPEGDataURL = %q, want %q", got, want)
	}
	if strings.ContainsAny(got, "\n\r") {
		t.Fatal("the data URL must not be wrapped")
	}
}
