package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
)

// ErrBadResponse is returned when the model answered but the answer could not
// be read.
//
// Port of the `badResponse` case in GemmaLLMService.swift. It is a THROWN error
// on purpose and only at the call sites that throw on iOS: see
// shared/parser/response-parsing.md § Failure semantics per call site, and the
// IdentifyAllItems comment below for the asymmetry that drives the scan
// engine's failure counting.
var ErrBadResponse = errors.New("the model's response could not be read")

// Sampling parameters, tuned rather than defaulted.
//
// These are the values eval/server.sh and eval/run_eval.py used to produce every
// recall number this project has, so they are part of the measurement, not a
// preference. top_k and top_p are passed explicitly because llama-server's
// defaults are not these, and a silent drift in sampling shows up as a recall
// regression that no test and no error message would ever point at.
const (
	// SamplingTemperature is 0.2: low, because this is extraction, not writing.
	SamplingTemperature = 0.2
	// SamplingTopK is 40.
	SamplingTopK = 40
	// SamplingTopP is 0.95.
	SamplingTopP = 0.95
)

// DefaultRequestTimeout bounds a single model call.
//
// Generous on purpose. On a Pi 5 the whole model is on the CPU and one vision
// call takes minutes; the iOS timing (about 20 s on an A18 Pro) is not the scale
// here. A timeout that fires on a slow-but-working box would look exactly like a
// broken model and would be counted as an inference failure.
const DefaultRequestTimeout = 10 * time.Minute

// DefaultModelAlias is the `model` field of the request. llama-server serves
// whatever GGUF it was started with and ignores this, but the OpenAI-compatible
// schema requires the key; eval/run_eval.py sends "gemma".
const DefaultModelAlias = "gemma"

// Client speaks to one llama-server generation over loopback HTTP.
//
// It is a value that the supervisor pins BY VALUE into each inference, alongside
// the epoch: the iOS adapter's second layer of safety is that an in-flight token
// stream holds its own strong reference to the client it started with, so
// clearing the client handle cannot tear a running stream out from under itself
// (GemmaLLMService.swift). Capturing this struct is the Go equivalent.
type Client struct {
	// BaseURL is the child's loopback root, e.g. http://127.0.0.1:41235.
	BaseURL string
	// APIKey is the per-generation random key the child was started with.
	APIKey string
	// HTTP is the transport. Never nil in practice; NewClient fills it in.
	HTTP *http.Client
	// Model is the `model` field of the request body.
	Model string
}

// NewClient builds a client with a transport configured for a long, local,
// single-slot call.
func NewClient(baseURL, apiKey string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = DefaultRequestTimeout
	}
	return &Client{
		BaseURL: strings.TrimSuffix(baseURL, "/"),
		APIKey:  apiKey,
		Model:   DefaultModelAlias,
		HTTP: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				// One slot, one connection. Keeping it alive avoids a fresh
				// handshake between the four calls of a scan.
				MaxIdleConns:        2,
				MaxIdleConnsPerHost: 2,
				IdleConnTimeout:     90 * time.Second,
				DisableCompression:  true,
			},
		},
	}
}

// Part is one element of a message's content array.
type Part struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// ImageURL carries a data: URL. llama-server accepts a base64 data URL here,
// which is how eval/run_eval.py has always sent images.
type ImageURL struct {
	URL string `json:"url"`
}

// TextPart builds a text content part.
func TextPart(s string) Part { return Part{Type: "text", Text: s} }

// ImagePart builds an image content part from encoded JPEG bytes.
func ImagePart(jpeg []byte) Part {
	return Part{Type: "image_url", ImageURL: &ImageURL{URL: JPEGDataURL(jpeg)}}
}

// JPEGDataURL renders JPEG bytes as a data: URL, the shape eval's data_url
// produces.
func JPEGDataURL(jpeg []byte) string {
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpeg)
}

// Message is one chat message. Exactly one user message is ever sent.
type Message struct {
	Role    string `json:"role"`
	Content []Part `json:"content"`
}

// chatRequest is the request body.
//
// What is DELIBERATELY ABSENT is as important as what is here:
//
//   - No response_format, no grammar, no GBNF, no json_schema. The eval harness
//     passes response_format, but it is a single-shot batch harness; this model
//     is RESIDENT and serves up to four sequential calls per scan.
//     shared/parser/response-parsing.md § PROHIBITION records what happens then:
//     the grammar sampler is never reset between calls, so call 1 returns clean
//     JSON and calls 2 to 4 return NOTHING. The scan loop swallows the
//     failures and the user gets "Nothing spotted" - and it would pass any
//     single-call smoke test. JSON comes from the prompt wording plus the
//     parser instead. Revisiting this is follow-up F1 and needs a multi-call
//     proof on a resident model, not a first-call one.
//   - No max_tokens and no stop strings. A truncated reply is unparsable, and
//     the parser's fence and channel salvage cannot recover a reply that was cut
//     off mid-object.
type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature"`
	TopK        int       `json:"top_k"`
	TopP        float64   `json:"top_p"`
	// CachePrompt is sent EXPLICITLY as false and must never gain an
	// omitempty tag, which would silently drop it (false is the zero value).
	//
	// This is the llama-server equivalent of resetSession(): with prompt caching
	// on, the server reuses the KV prefix of the previous request, so four
	// different images in one scan can share cached state that no longer
	// describes the image in front of the model. The iOS service resets the
	// session between requests for exactly this reason (build 8: "context reset
	// between requests"). Relying on the server default would leave the decision
	// to a flag on someone else's release notes.
	CachePrompt bool `json:"cache_prompt"`
}

// Completion is one model answer plus the numbers the bench reports.
type Completion struct {
	// Text is the raw content. It goes through the contract parser untouched:
	// channel stripping and fence salvage are the parser's job, not the
	// transport's.
	Text string
	// FinishReason is llama-server's stop reason.
	FinishReason string
	// PromptTokens and PredictedTokens come from the usage block.
	PromptTokens    int
	PredictedTokens int
	// PredictedPerSecond is decode throughput, from llama-server's timings
	// block. Step 77's bench reports it per call; it is 0 when absent.
	PredictedPerSecond float64
}

// chatResponse is the OpenAI-compatible reply, plus llama.cpp's timings.
type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Timings struct {
		PredictedPerSecond float64 `json:"predicted_per_second"`
	} `json:"timings"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// ChatPath is the endpoint. The OpenAI-compatible route is used rather than
// llama-server's native /completion because it is the one eval/run_eval.py
// proved against this GGUF, multimodal parts and all.
const ChatPath = "/v1/chat/completions"

// HealthPath is llama-server's readiness endpoint.
const HealthPath = "/health"

// Chat sends one user message built from parts and returns the raw completion.
func (c *Client) Chat(ctx context.Context, parts []Part) (Completion, error) {
	body := chatRequest{
		Model:       c.modelAlias(),
		Messages:    []Message{{Role: "user", Content: parts}},
		Temperature: SamplingTemperature,
		TopK:        SamplingTopK,
		TopP:        SamplingTopP,
		CachePrompt: false,
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return Completion{}, fmt.Errorf("encode chat request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+ChatPath, bytes.NewReader(encoded))
	if err != nil {
		return Completion{}, fmt.Errorf("build chat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return Completion{}, fmt.Errorf("chat request: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
	}()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxChatResponseBytes))
	if err != nil {
		return Completion{}, fmt.Errorf("read chat response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Completion{}, fmt.Errorf("llama-server returned %s: %s", resp.Status, snippet(raw))
	}

	var decoded chatResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return Completion{}, fmt.Errorf("decode chat response: %w", err)
	}
	if decoded.Error != nil && decoded.Error.Message != "" {
		return Completion{}, fmt.Errorf("llama-server error: %s", decoded.Error.Message)
	}
	if len(decoded.Choices) == 0 {
		// Zero choices is the shape a wedged grammar sampler produced on iOS.
		// It is an error, never an empty answer: an empty answer would be
		// reported as "the model saw nothing", which is the lie the whole
		// ScanOutcome split exists to prevent.
		return Completion{}, fmt.Errorf("llama-server returned no choices")
	}
	return Completion{
		Text:               decoded.Choices[0].Message.Content,
		FinishReason:       decoded.Choices[0].FinishReason,
		PromptTokens:       decoded.Usage.PromptTokens,
		PredictedTokens:    decoded.Usage.CompletionTokens,
		PredictedPerSecond: decoded.Timings.PredictedPerSecond,
	}, nil
}

// maxChatResponseBytes bounds a reply. A model answer is a few kilobytes; a
// megabyte is already pathological and 8 MiB is the point at which reading it is
// the failure.
const maxChatResponseBytes = 8 << 20

// IdentifyItem asks for ONE item from one image, and THROWS on a parse failure.
//
// The Completion is returned alongside the item, and on failure too, so the
// supervisor can carry llama-server's timings into /api/status and the bench
// without a second code path that re-implements the parse.
//
// Matches GemmaLLMService.identifyItem: the scan loop catches this, logs
// "[Scan] Crop i/N failed: ...", counts it as an inference failure, and
// continues to the next crop. A scan is never aborted by it.
func (c *Client) IdentifyItem(ctx context.Context, prompt string, imageJPEG []byte) (contract.IdentifiedItem, Completion, error) {
	completion, err := c.Chat(ctx, []Part{ImagePart(imageJPEG), TextPart(prompt)})
	if err != nil {
		return contract.IdentifiedItem{}, completion, err
	}
	item, ok := contract.DecodeIdentifiedItem(completion.Text)
	if !ok {
		return contract.IdentifiedItem{}, completion, fmt.Errorf("%w: %s", ErrBadResponse, snippet([]byte(completion.Text)))
	}
	return item, completion, nil
}

// IdentifyAllItems asks for every item in one image, and returns an EMPTY LIST
// with NO ERROR on a parse failure.
//
// The asymmetry against IdentifyItem is contractual, not an oversight
// (response-parsing.md § Failure semantics per call site: identifyAllItems
// "returns [], no error at all"), and the scan engine's failure counting is
// built on it: an unreadable-but-answered call is NOT an inference failure, so
// it must not push the scan towards Failed. A transport error still returns an
// error, because that call genuinely did not answer.
//
// The empty slice is deliberately non-nil, mirroring the parser's rule that a
// nil slice must never be used to mean "parse failed": "saw nothing" and "was
// unreadable" are different outcomes and only the first one reaches here.
func (c *Client) IdentifyAllItems(ctx context.Context, prompt string, imageJPEG []byte) ([]contract.IdentifiedItem, Completion, error) {
	completion, err := c.Chat(ctx, []Part{ImagePart(imageJPEG), TextPart(prompt)})
	if err != nil {
		return nil, completion, err
	}
	items, ok := contract.DecodeIdentifiedItemArray(completion.Text)
	if !ok {
		return []contract.IdentifiedItem{}, completion, nil
	}
	return items, completion, nil
}

// GenerateRecipes runs the text-only recipe call and throws on a parse failure.
//
// The caller turns that into the exact user-facing string the contract pins:
// "We could not read the recipe ideas this time. Please try again.", leaving the
// existing recipes untouched.
func (c *Client) GenerateRecipes(ctx context.Context, prompt string) (contract.RecipeBundleResponse, Completion, error) {
	completion, err := c.Chat(ctx, []Part{TextPart(prompt)})
	if err != nil {
		return contract.RecipeBundleResponse{}, completion, err
	}
	bundle, ok := contract.DecodeRecipeBundle(completion.Text)
	if !ok {
		return contract.RecipeBundleResponse{}, completion, fmt.Errorf("%w: %s", ErrBadResponse, snippet([]byte(completion.Text)))
	}
	return bundle, completion, nil
}

// Health reports whether the child is serving. llama-server answers
// {"status":"ok"} once the model is loaded, and 503 while it is still loading,
// which is the whole startup signal.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+HealthPath, nil)
	if err != nil {
		return err
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health %s: %s", resp.Status, snippet(body))
	}
	var payload struct {
		Status string `json:"status"`
	}
	// An unparsable body with a 200 is accepted: the status code is the
	// contract, the JSON is a courtesy.
	if err := json.Unmarshal(body, &payload); err == nil && payload.Status != "" && payload.Status != "ok" {
		return fmt.Errorf("health status %q", payload.Status)
	}
	return nil
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: DefaultRequestTimeout}
}

func (c *Client) modelAlias() string {
	if c.Model != "" {
		return c.Model
	}
	return DefaultModelAlias
}

// snippet bounds an error message so a megabyte of HTML from a misconfigured
// proxy cannot land in a log line or an API error.
func snippet(b []byte) string {
	const limit = 240
	s := strings.TrimSpace(string(b))
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "..."
}
