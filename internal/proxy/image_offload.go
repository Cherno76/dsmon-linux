package proxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	ImageOffloadActAbove    int64 = 40 * 1024 * 1024
	ImageOffloadTarget      int64 = 36 * 1024 * 1024
	FilesUploadLimit        int64 = 64 * 1024 * 1024
	FilesUploadTimeout            = 120 * time.Second
	ImageOffloadTotalBudget       = 60 * time.Second

	estimatedFileIDBytes     = 64
	fileIDExpiryMargin       = time.Minute
	defaultImageCacheEntries = 256
)

var supportedImageMediaTypes = map[string]bool{
	"image/jpeg": true,
	"image/jpg":  true,
	"image/png":  true,
	"image/gif":  true,
	"image/webp": true,
}

// 内联图片计入上游请求体上限。Codex 每轮 /v1/responses 都重发整段历史，
// 长会话里的 base64 截图很容易把请求顶到 48MiB 以上。
type imagePathStep struct {
	key     string
	index   int
	isIndex bool
}

type inlineImage struct {
	path        []imagePathStep
	raw         string
	mediaType   string
	inlineBytes int
}

type inlineImageCandidate struct {
	raw         string
	hash        string
	mediaType   string
	base64      string
	byteCount   int
	inlineBytes int
}

type inlineImageTarget struct {
	path      []imagePathStep
	candidate inlineImageCandidate
}

type inlineImageResolution struct {
	target inlineImageTarget
	fileID string
}

type inlineImagePlan struct {
	targets   []inlineImageTarget
	bodyBytes int
}

type preparedInlineImages struct {
	plan inlineImagePlan
	tree any
}

type imageFileIDEntry struct {
	id        string
	expiresAt *time.Time
}

// ImageFileIDCache 只驻留内存：Files API 文件 30 天过期，持久化旧 id 最终只会换来 400。
type ImageFileIDCache struct {
	mu      sync.Mutex
	entries map[string]imageFileIDEntry
	order   []string
	cap     int
	now     func() time.Time
}

func NewImageFileIDCache(capacity int) *ImageFileIDCache {
	if capacity <= 0 {
		capacity = defaultImageCacheEntries
	}
	return &ImageFileIDCache{
		entries: make(map[string]imageFileIDEntry),
		cap:     capacity,
		now:     time.Now,
	}
}

func (c *ImageFileIDCache) fileID(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return "", false
	}
	if entry.expiresAt != nil && !entry.expiresAt.Add(-fileIDExpiryMargin).After(c.now()) {
		c.removeLocked(key)
		return "", false
	}
	return entry.id, true
}

func (c *ImageFileIDCache) store(id string, expiresAt *time.Time, key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists {
		c.order = append(c.order, key)
	}
	c.entries[key] = imageFileIDEntry{id: id, expiresAt: expiresAt}
	for len(c.order) > c.cap {
		c.removeLocked(c.order[0])
	}
}

func (c *ImageFileIDCache) remove(ids []string) {
	if len(ids) == 0 {
		return
	}
	doomed := make(map[string]bool, len(ids))
	for _, id := range ids {
		doomed[id] = true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, entry := range c.entries {
		if doomed[entry.id] {
			c.removeLocked(key)
		}
	}
}

func (c *ImageFileIDCache) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func (c *ImageFileIDCache) removeLocked(key string) {
	delete(c.entries, key)
	for index, item := range c.order {
		if item == key {
			c.order = append(c.order[:index], c.order[index+1:]...)
			return
		}
	}
}

type uploadedImageFile struct {
	id        string
	expiresAt time.Time
}

type imageOffloadResult struct {
	body        []byte
	changed     bool
	moved       int
	uploaded    int
	savedBytes  int
	skipped     int
	failed      int
	fileIDs     []string
	failureNote string
}

type largeImageOffloader struct {
	transport http.RoundTripper
	cache     *ImageFileIDCache
	now       func() time.Time

	actAbove int64
	target   int64
	budget   time.Duration
}

func newLargeImageOffloader(transport http.RoundTripper) *largeImageOffloader {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &largeImageOffloader{
		transport: transport,
		cache:     NewImageFileIDCache(defaultImageCacheEntries),
		now:       time.Now,
		actAbove:  ImageOffloadActAbove,
		target:    ImageOffloadTarget,
		budget:    ImageOffloadTotalBudget,
	}
}

func bareImageMediaType(mediaType string) string {
	if before, _, ok := strings.Cut(mediaType, ";"); ok {
		mediaType = before
	}
	return strings.ToLower(strings.TrimSpace(mediaType))
}

func imageMediaTypeOfDataURL(raw string) (string, bool) {
	if !strings.HasPrefix(raw, "data:image/") {
		return "", false
	}
	comma := strings.IndexByte(raw, ',')
	if comma < 0 {
		return "", false
	}
	header := raw[:comma]
	if !strings.HasSuffix(header, ";base64") {
		return "", false
	}
	mediaType := header[len("data:") : len(header)-len(";base64")]
	if len(mediaType) <= len("image/") || strings.HasSuffix(mediaType, "/") {
		return "", false
	}
	return mediaType, true
}

func parseImageDataURL(raw string) (mediaType, encoded string, ok bool) {
	mediaType, ok = imageMediaTypeOfDataURL(raw)
	if !ok {
		return "", "", false
	}
	comma := strings.IndexByte(raw, ',')
	return mediaType, raw[comma+1:], true
}

func decodeImageBase64(text string) ([]byte, bool) {
	if decoded, err := base64.StdEncoding.DecodeString(text); err == nil {
		return decoded, true
	}
	stripped := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, text)
	if stripped == text {
		return nil, false
	}
	decoded, err := base64.StdEncoding.DecodeString(stripped)
	if err != nil {
		return nil, false
	}
	return decoded, true
}

func imageCandidateFromDataURL(raw string) (inlineImageCandidate, bool) {
	mediaType, encoded, ok := parseImageDataURL(raw)
	if !ok {
		return inlineImageCandidate{}, false
	}
	decoded, ok := decodeImageBase64(encoded)
	if !ok {
		return inlineImageCandidate{}, false
	}
	sum := sha256.Sum256(decoded)
	return inlineImageCandidate{
		raw:         raw,
		hash:        hex.EncodeToString(sum[:]),
		mediaType:   mediaType,
		base64:      encoded,
		byteCount:   len(decoded),
		inlineBytes: len(raw),
	}, true
}

func findInlineImages(root any) []inlineImage {
	var found []inlineImage
	walkJSON(root, nil, func(object map[string]any, path []imagePathStep) {
		for _, raw := range inlineImageStrings(object) {
			mediaType, ok := imageMediaTypeOfDataURL(raw)
			if !ok || !supportedImageMediaTypes[bareImageMediaType(mediaType)] {
				continue
			}
			found = append(found, inlineImage{
				path:        path,
				raw:         raw,
				mediaType:   mediaType,
				inlineBytes: len(raw),
			})
		}
	})
	return found
}

func inlineImageStrings(object map[string]any) []string {
	kind, _ := object["type"].(string)
	switch kind {
	case "input_image":
		if raw, ok := object["image_url"].(string); ok {
			return []string{raw}
		}
	case "image_url":
		inner, _ := object["image_url"].(map[string]any)
		if raw, ok := inner["url"].(string); ok {
			return []string{raw}
		}
	case "file":
		if raw, ok := object["file_data"].(string); ok {
			return []string{raw}
		}
	}
	return nil
}

func walkJSON(node any, path []imagePathStep, visit func(map[string]any, []imagePathStep)) {
	switch value := node.(type) {
	case map[string]any:
		visit(value, path)
		for key, child := range value {
			walkJSON(child, appendImagePath(path, imagePathStep{key: key}), visit)
		}
	case []any:
		for index, child := range value {
			walkJSON(child, appendImagePath(path, imagePathStep{index: index, isIndex: true}), visit)
		}
	}
}

func appendImagePath(path []imagePathStep, step imagePathStep) []imagePathStep {
	next := make([]imagePathStep, len(path)+1)
	copy(next, path)
	next[len(path)] = step
	return next
}

func prepareInlineImages(body []byte, actAbove, target int64) (*preparedInlineImages, bool) {
	if int64(len(body)) <= actAbove {
		return nil, false
	}
	tree, ok := decodeJSONTree(body)
	if !ok {
		return nil, false
	}
	found := findInlineImages(tree)
	if len(found) == 0 {
		return nil, false
	}
	sort.SliceStable(found, func(i, j int) bool {
		return found[i].inlineBytes > found[j].inlineBytes
	})

	estimate := int64(len(body))
	chosen := make([]inlineImage, 0, len(found))
	for _, image := range found {
		if estimate <= target {
			break
		}
		chosen = append(chosen, image)
		saved := image.inlineBytes - estimatedFileIDBytes
		if saved > 0 {
			estimate -= int64(saved)
		}
	}
	if len(chosen) == 0 {
		return nil, false
	}

	targets := make([]inlineImageTarget, 0, len(chosen))
	for _, image := range chosen {
		candidate, ok := imageCandidateFromDataURL(image.raw)
		if !ok {
			continue
		}
		targets = append(targets, inlineImageTarget{path: image.path, candidate: candidate})
	}
	if len(targets) == 0 {
		return nil, false
	}
	return &preparedInlineImages{
		plan: inlineImagePlan{targets: targets, bodyBytes: len(body)},
		tree: tree,
	}, true
}

func decodeJSONTree(body []byte) (any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var tree any
	if err := decoder.Decode(&tree); err != nil {
		return nil, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, false
	}
	return tree, true
}

func applyInlineImages(prepared *preparedInlineImages,
	resolutions []inlineImageResolution) ([]byte, int, bool) {
	if prepared == nil || len(resolutions) == 0 {
		return nil, 0, false
	}
	tree := prepared.tree
	replaced := 0
	for _, resolution := range resolutions {
		object, ok := jsonValueAt(tree, resolution.target.path).(map[string]any)
		if !ok {
			continue
		}
		rewritten, ok := rewriteInlineImageObject(object,
			resolution.target.candidate.raw, resolution.fileID)
		if !ok {
			continue
		}
		tree = setJSONValueAt(tree, resolution.target.path, rewritten)
		replaced++
	}
	if replaced == 0 {
		return nil, 0, false
	}
	body, ok := encodeJSONTree(tree)
	if !ok {
		return nil, 0, false
	}
	return body, replaced, true
}

func rewriteInlineImageObject(object map[string]any, expected, fileID string) (map[string]any, bool) {
	kind, _ := object["type"].(string)
	switch kind {
	case "input_image":
		if raw, _ := object["image_url"].(string); raw != expected {
			return nil, false
		}
		out := cloneJSONObject(object)
		delete(out, "image_url")
		out["file_id"] = fileID
		return out, true
	case "image_url":
		inner, _ := object["image_url"].(map[string]any)
		if raw, _ := inner["url"].(string); raw != expected {
			return nil, false
		}
		return map[string]any{"type": "file", "file_id": fileID}, true
	case "file":
		if raw, _ := object["file_data"].(string); raw != expected {
			return nil, false
		}
		out := cloneJSONObject(object)
		delete(out, "file_data")
		delete(out, "filename")
		out["file_id"] = fileID
		return out, true
	default:
		return nil, false
	}
}

func cloneJSONObject(object map[string]any) map[string]any {
	out := make(map[string]any, len(object))
	for key, value := range object {
		out[key] = value
	}
	return out
}

func jsonValueAt(node any, path []imagePathStep) any {
	if len(path) == 0 {
		return node
	}
	step := path[0]
	if step.isIndex {
		array, ok := node.([]any)
		if !ok || step.index < 0 || step.index >= len(array) {
			return nil
		}
		return jsonValueAt(array[step.index], path[1:])
	}
	object, ok := node.(map[string]any)
	if !ok {
		return nil
	}
	child, ok := object[step.key]
	if !ok {
		return nil
	}
	return jsonValueAt(child, path[1:])
}

func setJSONValueAt(node any, path []imagePathStep, value any) any {
	if len(path) == 0 {
		return value
	}
	step := path[0]
	if step.isIndex {
		array, ok := node.([]any)
		if !ok || step.index < 0 || step.index >= len(array) {
			return node
		}
		out := append([]any(nil), array...)
		out[step.index] = setJSONValueAt(array[step.index], path[1:], value)
		return out
	}
	object, ok := node.(map[string]any)
	if !ok {
		return node
	}
	child, ok := object[step.key]
	if !ok {
		return node
	}
	out := cloneJSONObject(object)
	out[step.key] = setJSONValueAt(child, path[1:], value)
	return out
}

func encodeJSONTree(tree any) ([]byte, bool) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(tree); err != nil {
		return nil, false
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), true
}

func imageCacheFingerprint(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:])[:8]
}

func imageCacheKeyPrefix(providerID, apiKey string) string {
	return providerID + "|" + imageCacheFingerprint(apiKey) + "|"
}

func imageCacheKey(providerID, apiKey, imageHash string) string {
	return imageCacheKeyPrefix(providerID, apiKey) + imageHash
}

func filesAPIEndpoint(base string) (string, bool) {
	base = strings.TrimSpace(base)
	if base == "" {
		return "", false
	}
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", false
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/files"
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), true
}

func imageFilename(mediaType string) string {
	switch bareImageMediaType(mediaType) {
	case "image/jpeg", "image/jpg":
		return "image.jpg"
	case "image/gif":
		return "image.gif"
	case "image/webp":
		return "image.webp"
	default:
		return "image.png"
	}
}

func imageMultipartBody(imageBytes []byte, mediaType, boundary string) ([]byte, error) {
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	if err := writer.SetBoundary(boundary); err != nil {
		return nil, fmt.Errorf("set multipart boundary: %w", err)
	}
	for _, field := range [][2]string{
		{"purpose", "user_data"},
		{"expires_after[anchor]", "created_at"},
		{"expires_after[seconds]", "2592000"},
	} {
		if err := writer.WriteField(field[0], field[1]); err != nil {
			return nil, fmt.Errorf("write multipart field %s: %w", field[0], err)
		}
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition",
		fmt.Sprintf(`form-data; name="file"; filename="%s"`, imageFilename(mediaType)))
	header.Set("Content-Type", bareImageMediaType(mediaType))
	part, err := writer.CreatePart(header)
	if err != nil {
		return nil, fmt.Errorf("create multipart image part: %w", err)
	}
	if _, err := part.Write(imageBytes); err != nil {
		return nil, fmt.Errorf("write multipart image: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close multipart body: %w", err)
	}
	return buffer.Bytes(), nil
}

func randomMultipartBoundary() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("dsmon-%d", time.Now().UnixNano())
	}
	return "dsmon-" + hex.EncodeToString(raw[:])
}

func (o *largeImageOffloader) upload(ctx context.Context, imageBytes []byte, mediaType,
	base, apiKey string, timeout time.Duration) (uploadedImageFile, error) {
	if int64(len(imageBytes)) > FilesUploadLimit {
		return uploadedImageFile{}, fmt.Errorf("image is %d bytes, over Files API limit", len(imageBytes))
	}
	endpoint, ok := filesAPIEndpoint(base)
	if !ok {
		return uploadedImageFile{}, errors.New("invalid Files API base URL")
	}
	boundary := randomMultipartBoundary()
	body, err := imageMultipartBody(imageBytes, mediaType, boundary)
	if err != nil {
		return uploadedImageFile{}, err
	}
	if timeout <= 0 {
		timeout = FilesUploadTimeout
	}
	uploadCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(uploadCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return uploadedImageFile{}, fmt.Errorf("create Files API request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)

	response, err := o.transport.RoundTrip(request)
	if err != nil {
		return uploadedImageFile{}, fmt.Errorf("upload image: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
	if err != nil {
		return uploadedImageFile{}, fmt.Errorf("read Files API response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return uploadedImageFile{}, fmt.Errorf("Files API returned HTTP %d", response.StatusCode)
	}
	uploaded, ok := decodeUploadedImage(responseBody, o.now())
	if !ok {
		return uploadedImageFile{}, errors.New("Files API response has no file_id")
	}
	return uploaded, nil
}

func decodeUploadedImage(body []byte, now time.Time) (uploadedImageFile, bool) {
	var payload struct {
		ID        string      `json:"id"`
		ExpiresAt json.Number `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &payload); err != nil ||
		!strings.HasPrefix(payload.ID, "file-api-") {
		return uploadedImageFile{}, false
	}
	expiresAt := now.Add(2592000 * time.Second)
	if seconds, err := payload.ExpiresAt.Int64(); err == nil && seconds > 0 {
		expiresAt = time.Unix(seconds, 0)
	}
	return uploadedImageFile{id: payload.ID, expiresAt: expiresAt}, true
}

func (o *largeImageOffloader) run(ctx context.Context, body []byte, base, apiKey,
	providerID string, onUpload func(int)) imageOffloadResult {
	result := imageOffloadResult{body: body}
	if apiKey == "" {
		return result
	}
	prepared, ok := prepareInlineImages(body, o.actAbove, o.target)
	if !ok {
		return result
	}

	started := o.now()
	resolutions := make([]inlineImageResolution, 0, len(prepared.plan.targets))
	usedIDs := make([]string, 0, len(prepared.plan.targets))
	failures := make([]string, 0)
	cachePrefix := imageCacheKeyPrefix(providerID, apiKey)
	for _, target := range prepared.plan.targets {
		key := cachePrefix + target.candidate.hash
		if cached, ok := o.cache.fileID(key); ok {
			resolutions = append(resolutions, inlineImageResolution{target: target, fileID: cached})
			usedIDs = append(usedIDs, cached)
			continue
		}
		elapsed := o.now().Sub(started)
		if elapsed >= o.budget {
			result.skipped++
			continue
		}
		imageBytes, ok := decodeImageBase64(target.candidate.base64)
		if !ok {
			failures = append(failures, "base64 decode failed")
			continue
		}
		timeout := FilesUploadTimeout
		if remaining := o.budget - elapsed; remaining < timeout {
			timeout = remaining
		}
		if timeout < time.Second {
			timeout = time.Second
		}
		uploaded, err := o.upload(ctx, imageBytes, bareImageMediaType(target.candidate.mediaType),
			base, apiKey, timeout)
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		resolutions = append(resolutions,
			inlineImageResolution{target: target, fileID: uploaded.id})
		usedIDs = append(usedIDs, uploaded.id)
		expiresAt := uploaded.expiresAt
		o.cache.store(uploaded.id, &expiresAt, key)
		result.uploaded++
		if onUpload != nil {
			onUpload(len(imageBytes))
		}
	}

	if len(resolutions) == 0 {
		if len(failures) > 0 {
			result.failureNote = strings.Join(failures, "; ")
		} else if result.skipped > 0 {
			result.failureNote = fmt.Sprintf("upload budget exhausted, skipped %d images", result.skipped)
		}
		result.failed = len(failures)
		return result
	}
	rewritten, replaced, ok := applyInlineImages(prepared, resolutions)
	if !ok {
		result.failed = len(failures)
		result.failureNote = "failed to rewrite request body"
		return result
	}
	result.body = rewritten
	result.changed = true
	result.moved = replaced
	result.savedBytes = len(body) - len(rewritten)
	if result.savedBytes < 0 {
		result.savedBytes = 0
	}
	result.failed = len(failures)
	if len(failures) > 0 {
		result.failureNote = failures[0]
	}
	result.fileIDs = usedIDs
	return result
}

func isMissingFileIDError(statusCode int, upstreamText string) bool {
	if statusCode != http.StatusBadRequest && statusCode != http.StatusUnprocessableEntity {
		return false
	}
	text := strings.ToLower(upstreamText)
	if !strings.Contains(text, "file_id") {
		return false
	}
	return strings.Contains(text, "do not exist") ||
		strings.Contains(text, "not created under your account") ||
		strings.Contains(text, "invalid file_id")
}

func isUpstreamBodyLimit(statusCode int, upstreamText string) bool {
	if statusCode != http.StatusRequestEntityTooLarge {
		return false
	}
	text := strings.ToLower(upstreamText)
	return strings.Contains(text, "failed to buffer the request body") &&
		strings.Contains(text, "length limit exceeded")
}
