package proxy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type imageRoundTripFunc func(*http.Request) (*http.Response, error)

func (f imageRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func testImageDataURL(byteCount int, mediaType string) string {
	if mediaType == "" {
		mediaType = "image/png"
	}
	raw := bytes.Repeat([]byte{0xab}, byteCount)
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(raw)
}

func testResponsesImageBody(images ...string) []byte {
	content := make([]any, 0, len(images)+1)
	content = append(content, map[string]any{"type": "input_text", "text": "look"})
	for _, image := range images {
		content = append(content, map[string]any{
			"type": "input_image", "image_url": image, "detail": "high",
		})
	}
	body, _ := json.Marshal(map[string]any{
		"model": "deepseek-chat",
		"input": []any{map[string]any{"role": "user", "content": content}},
	})
	return body
}

func decodeTestObject(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		t.Fatalf("decode body: %v\n%s", err, body)
	}
	return object
}

func imageResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestInlineImageOffloadRejectsNonImageDataURLs(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "png", raw: testImageDataURL(8, "image/png"), want: true},
		{name: "jpeg", raw: testImageDataURL(8, "image/jpeg"), want: true},
		{name: "external URL", raw: "https://example.com/a.png"},
		{name: "not base64", raw: "data:image/png,abc"},
		{name: "not image", raw: "data:application/pdf;base64,AAAA"},
		{name: "missing comma", raw: "data:image/png;base64"},
		{name: "empty subtype", raw: "data:image/;base64,AAAA"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, ok := parseImageDataURL(test.raw)
			if ok != test.want {
				t.Fatalf("parseImageDataURL(%q) ok=%v, want %v", test.raw, ok, test.want)
			}
		})
	}
}

func TestInlineImageOffloadFindsAllDocumentedShapes(t *testing.T) {
	url := testImageDataURL(64, "image/png")
	tests := []struct {
		name string
		body []byte
	}{
		{
			name: "responses",
			body: []byte(`{"input":[{"content":[{"type":"input_image","image_url":"` + url + `"}]}]}`),
		},
		{
			name: "chat completions",
			body: []byte(`{"messages":[{"content":[{"type":"image_url","image_url":{"url":"` + url + `"}}]}]}`),
		},
		{
			name: "inline file",
			body: []byte(`{"messages":[{"content":[{"type":"file","file_data":"` + url + `","filename":"a.png"}]}]}`),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prepared, ok := prepareInlineImages(test.body, 10, 10)
			if !ok || len(prepared.plan.targets) != 1 {
				t.Fatalf("prepared=%+v ok=%v, want one target", prepared, ok)
			}
			if prepared.plan.targets[0].candidate.inlineBytes != len(url) {
				t.Fatalf("inline bytes = %d, want %d", prepared.plan.targets[0].candidate.inlineBytes, len(url))
			}
		})
	}
}

func TestInlineImageOffloadRewritesAllDocumentedShapes(t *testing.T) {
	url := testImageDataURL(128, "image/jpeg")
	tests := []struct {
		name string
		body []byte
		part func(map[string]any) map[string]any
	}{
		{
			name: "responses",
			body: []byte(`{"input":[{"content":[{"type":"input_image","image_url":"` + url + `","detail":"original"}]}]}`),
			part: func(root map[string]any) map[string]any {
				input := root["input"].([]any)
				content := input[0].(map[string]any)["content"].([]any)
				return content[0].(map[string]any)
			},
		},
		{
			name: "chat completions",
			body: []byte(`{"messages":[{"content":[{"type":"image_url","image_url":{"url":"` + url + `"}}]}]}`),
			part: func(root map[string]any) map[string]any {
				messages := root["messages"].([]any)
				content := messages[0].(map[string]any)["content"].([]any)
				return content[0].(map[string]any)
			},
		},
		{
			name: "inline file",
			body: []byte(`{"messages":[{"content":[{"type":"file","file_data":"` + url + `","filename":"shot.jpg"}]}]}`),
			part: func(root map[string]any) map[string]any {
				messages := root["messages"].([]any)
				content := messages[0].(map[string]any)["content"].([]any)
				return content[0].(map[string]any)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prepared, ok := prepareInlineImages(test.body, 10, 10)
			if !ok {
				t.Fatal("expected image offload plan")
			}
			rewritten, replaced, ok := applyInlineImages(prepared, []inlineImageResolution{{
				target: prepared.plan.targets[0], fileID: "file-api-test",
			}})
			if !ok || replaced != 1 {
				t.Fatalf("apply ok=%v replaced=%d", ok, replaced)
			}
			if bytes.Contains(rewritten, []byte("data:image")) {
				t.Fatalf("rewritten body still contains inline image: %s", rewritten)
			}
			part := test.part(decodeTestObject(t, rewritten))
			if part["file_id"] != "file-api-test" {
				t.Fatalf("part = %#v", part)
			}
			if _, ok := part["image_url"]; ok {
				t.Fatalf("image_url survived rewrite: %#v", part)
			}
			if _, ok := part["file_data"]; ok {
				t.Fatalf("file_data survived rewrite: %#v", part)
			}
			if _, ok := part["filename"]; ok {
				t.Fatalf("filename survived rewrite: %#v", part)
			}
		})
	}
}

func TestInlineImageOffloadMovesLargestImagesFirst(t *testing.T) {
	small := testImageDataURL(30, "image/png")
	medium := testImageDataURL(300, "image/png")
	large := testImageDataURL(3000, "image/png")
	body := testResponsesImageBody(small, medium, large)

	plan, ok := prepareInlineImages(body,
		int64(len(body)-1), int64(len(body)-(len(large)-estimatedFileIDBytes)+1))
	if !ok || len(plan.plan.targets) != 1 ||
		plan.plan.targets[0].candidate.inlineBytes != len(large) {
		t.Fatalf("plan = %+v, want only the largest image", plan)
	}
}

func TestInlineImageOffloadIgnoresUnsupportedAndExistingFileIDs(t *testing.T) {
	heic := testImageDataURL(4096, "image/heic")
	body := testResponsesImageBody(heic)
	if prepared, ok := prepareInlineImages(body, 10, 10); ok {
		t.Fatalf("unsupported image produced a plan: %+v", prepared)
	}

	existing := []byte(`{"input":[{"content":[{"type":"input_image","file_id":"file-api-old"}]}]}`)
	if prepared, ok := prepareInlineImages(existing, 1, 1); ok {
		t.Fatalf("existing file_id produced a plan: %+v", prepared)
	}
}

func TestInlineImageBase64ToleratesWhitespaceButNotGarbage(t *testing.T) {
	raw := bytes.Repeat([]byte{0x5a}, 96)
	encoded := base64.StdEncoding.EncodeToString(raw)
	wrapped := encoded[:20] + "\n" + encoded[20:]
	decoded, ok := decodeImageBase64(wrapped)
	if !ok || !bytes.Equal(decoded, raw) {
		t.Fatalf("whitespace base64 decoded=%v ok=%v", decoded, ok)
	}
	if _, ok := decodeImageBase64(encoded + "!!!!"); ok {
		t.Fatal("invalid base64 was accepted")
	}
}

func TestImageOffloaderUploadsOnceAndReusesCache(t *testing.T) {
	var uploads int
	var uploadedBytes int
	transport := imageRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/files" {
			t.Fatalf("upload path = %q", request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Fatalf("authorization = %q", got)
		}
		form, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(form, []byte(`name="purpose"`)) ||
			!bytes.Contains(form, []byte(`name="file"`)) {
			t.Fatalf("multipart form missing fields: %s", form)
		}
		uploads++
		return imageResponse(http.StatusOK,
			fmt.Sprintf(`{"id":"file-api-%d"}`, uploads)), nil
	})
	offloader := newLargeImageOffloader(transport)
	offloader.actAbove = 10
	offloader.target = 5
	offloader.budget = time.Second

	body := testResponsesImageBody(
		testImageDataURL(4000, "image/png"),
		testImageDataURL(3000, "image/jpeg"),
	)
	first := offloader.run(context.Background(), body, "https://api.deepseek.com", "sk-test",
		"deepseek", func(bytes int) { uploadedBytes += bytes })
	if !first.changed || first.moved != 2 || first.uploaded != 2 ||
		first.savedBytes <= 0 || uploads != 2 || uploadedBytes != 7000 {
		t.Fatalf("first = %+v uploads=%d bytes=%d", first, uploads, uploadedBytes)
	}
	if bytes.Contains(first.body, []byte("data:image")) ||
		!bytes.Contains(first.body, []byte("file-api-1")) ||
		!bytes.Contains(first.body, []byte("file-api-2")) {
		t.Fatalf("rewritten body = %s", first.body)
	}

	second := offloader.run(context.Background(), body, "https://api.deepseek.com", "sk-test",
		"deepseek", func(bytes int) { uploadedBytes += bytes })
	if !second.changed || second.moved != 2 || second.uploaded != 0 ||
		uploads != 2 || uploadedBytes != 7000 {
		t.Fatalf("second = %+v uploads=%d bytes=%d", second, uploads, uploadedBytes)
	}
}

func TestImageOffloaderFallsBackToOriginalBodyOnUploadFailure(t *testing.T) {
	transport := imageRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return imageResponse(http.StatusInternalServerError, "boom"), nil
	})
	offloader := newLargeImageOffloader(transport)
	offloader.actAbove = 10
	offloader.target = 5
	body := testResponsesImageBody(testImageDataURL(4000, "image/png"))

	result := offloader.run(context.Background(), body, "https://api.deepseek.com", "sk-test",
		"deepseek", nil)
	if result.changed || !bytes.Equal(result.body, body) {
		t.Fatalf("failed offload changed the body: %+v", result)
	}
	if !strings.Contains(result.failureNote, "500") {
		t.Fatalf("failure note = %q", result.failureNote)
	}
}

func TestImageMultipartBodyUsesDocumentedFields(t *testing.T) {
	body, err := imageMultipartBody([]byte{0x89, 0x50, 0x4e, 0x47},
		"image/jpeg;charset=utf-8", "BOUND")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		`name="purpose"`,
		"user_data",
		`name="file"; filename="image.jpg"`,
		"Content-Type: image/jpeg\r\n",
		"name=\"expires_after[anchor]\"",
		"2592000",
		"--BOUND--\r\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("multipart body missing %q:\n%s", want, text)
		}
	}
}

func TestImageFileIDCacheExpiryScopeAndRemoval(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	cache := NewImageFileIDCache(2)
	cache.now = func() time.Time { return now }
	key := imageCacheKey("deepseek", "sk-secret", "same-image")
	expires := now.Add(30 * time.Second)
	cache.store("file-api-old", &expires, key)
	if _, ok := cache.fileID(key); ok {
		t.Fatal("near-expiry file_id was reused")
	}

	fresh := now.Add(time.Hour)
	cache.store("file-api-fresh", &fresh, key)
	if got, ok := cache.fileID(key); !ok || got != "file-api-fresh" {
		t.Fatalf("fresh file_id = %q ok=%v", got, ok)
	}
	otherKey := imageCacheKey("other", "sk-secret", "same-image")
	if _, ok := cache.fileID(otherKey); ok {
		t.Fatal("cache entry leaked across providers")
	}
	if strings.Contains(key, "sk-secret") {
		t.Fatalf("cache key contains plaintext API key: %q", key)
	}
	cache.remove([]string{"file-api-fresh"})
	if cache.count() != 0 {
		t.Fatalf("cache count = %d after removal", cache.count())
	}
}

func TestInvalidFileIDDetectionMatchesOnlyTheRealError(t *testing.T) {
	real := `{"error":{"message":"the following file_ids do not exist or are not created under your account: file-api-1"}}`
	if !isMissingFileIDError(http.StatusBadRequest, real) {
		t.Fatal("real missing file_id error was not recognized")
	}
	if !isMissingFileIDError(http.StatusUnprocessableEntity, "invalid file_id") {
		t.Fatal("422 invalid file_id was not recognized")
	}
	if isMissingFileIDError(http.StatusBadRequest,
		`{"error":{"message":"input_image cannot have both image_url and file_id"}}`) {
		t.Fatal("shape error was mistaken for a missing file_id")
	}
	if isMissingFileIDError(http.StatusTooManyRequests, real) {
		t.Fatal("non-400/422 status was mistaken for a missing file_id")
	}
}

func TestUpstreamBodyLimitDetectionMatchesOnlyTheUpstreamError(t *testing.T) {
	upstream := "Failed to buffer the request body: length limit exceeded"
	if !isUpstreamBodyLimit(http.StatusRequestEntityTooLarge, upstream) ||
		!isUpstreamBodyLimit(http.StatusRequestEntityTooLarge, strings.ToUpper(upstream)) {
		t.Fatal("upstream body limit error was not recognized")
	}
	if isUpstreamBodyLimit(http.StatusRequestEntityTooLarge,
		"request body exceeds 50MiB limit") {
		t.Fatal("local 413 was mistaken for the upstream limit")
	}
	if isUpstreamBodyLimit(http.StatusOK, upstream) {
		t.Fatal("non-413 was mistaken for the upstream limit")
	}
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	clear(buffer)
	return len(buffer), nil
}

func TestDefaultRequestLimitCoversLongAgentSessions(t *testing.T) {
	const longSessionBody = int64(23_451_261)
	if MaxRequestBodyBytes < 50*1024*1024 || longSessionBody > MaxRequestBodyBytes {
		t.Fatalf("request body limit = %d", MaxRequestBodyBytes)
	}
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:18080/v1/responses",
		io.LimitReader(zeroReader{}, longSessionBody))
	request.ContentLength = longSessionBody
	body, err := readRequestBody(httptest.NewRecorder(), request, MaxRequestBodyBytes)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(body)) != longSessionBody {
		t.Fatalf("read %d bytes, want %d", len(body), longSessionBody)
	}
}
