package proxy

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type Protocol string

const (
	ProtocolOpenAI    Protocol = "openai"
	ProtocolAnthropic Protocol = "anthropic"
)

func DetectProtocol(path string, headers http.Header) Protocol {
	lowerPath := strings.ToLower(path)
	if strings.Contains(lowerPath, "/v1/messages") || strings.Contains(lowerPath, "/v1/complete") {
		return ProtocolAnthropic
	}
	for key := range headers {
		if strings.EqualFold(key, "anthropic-version") {
			return ProtocolAnthropic
		}
	}
	return ProtocolOpenAI
}

func NormalizeEndpoint(path string) string {
	lowerPath := strings.ToLower(path)
	switch {
	case strings.Contains(lowerPath, "/v1/tools/search"):
		return "/v1/tools/search"
	case strings.Contains(lowerPath, "/v1/tools/fetch"):
		return "/v1/tools/fetch"
	case strings.Contains(lowerPath, "/v1/messages"):
		return "/v1/messages"
	case strings.Contains(lowerPath, "/v1/responses"):
		return "/v1/responses"
	case strings.Contains(lowerPath, "/chat/completions"):
		return "/v1/chat/completions"
	default:
		return path
	}
}

func TargetURL(base, apiPath, requestPath string) (*url.URL, error) {
	baseURL, err := url.Parse(base)
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		if err == nil {
			err = fmt.Errorf("base URL needs scheme and host")
		}
		return nil, fmt.Errorf("parse upstream base %q: %w", base, err)
	}
	requestURL, err := url.ParseRequestURI(requestPath)
	if err != nil {
		return nil, fmt.Errorf("parse request path %q: %w", requestPath, err)
	}
	apiPath = normalizeAPIPath(apiPath)
	path := requestURL.EscapedPath()
	if apiPath != "/" && strings.HasPrefix(path, apiPath) {
		path = strings.TrimPrefix(path, apiPath)
	}
	if path == "" {
		path = "/"
	} else if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	fullPath := apiPath + path
	if apiPath == "/" {
		fullPath = path
	}
	if baseIncludesAPIPath(baseURL.Path, apiPath) {
		baseURL.Path = joinURLPath(baseURL.Path, path)
	} else {
		baseURL.Path = joinURLPath(baseURL.Path, fullPath)
	}
	baseURL.RawPath = ""
	baseURL.RawQuery = requestURL.RawQuery
	return baseURL, nil
}

func baseIncludesAPIPath(basePath, apiPath string) bool {
	basePath = strings.TrimSuffix(basePath, "/")
	apiPath = strings.TrimSuffix(apiPath, "/")
	if apiPath == "" || apiPath == "/" || !strings.HasSuffix(basePath, apiPath) {
		return false
	}
	// apiPath 已规范成以 / 开头，所以后缀匹配本身就保证路径段边界。
	return true
}

func normalizeAPIPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = "/" + strings.Trim(value, "/")
	return value
}

func joinURLPath(basePath, requestPath string) string {
	basePath = strings.TrimSuffix(basePath, "/")
	if requestPath == "" {
		return basePath
	}
	if !strings.HasPrefix(requestPath, "/") {
		requestPath = "/" + requestPath
	}
	return basePath + requestPath
}

func ShouldForwardRequestHeader(name string) bool {
	switch strings.ToLower(name) {
	case "host", "content-length", "transfer-encoding", "authorization", "x-api-key",
		"connection", "proxy-connection", "keep-alive", "te", "trailer", "upgrade",
		"proxy-authenticate", "proxy-authorization":
		return false
	default:
		return true
	}
}

func ClientUsesAPIKeyHeader(headers http.Header) bool {
	for key := range headers {
		if strings.EqualFold(key, "x-api-key") {
			return true
		}
	}
	return false
}

func BuildUpstreamRequest(ctx context.Context, method, requestPath string, headers http.Header,
	body []byte, base, apiPath, apiKey, authPrefix string) (*http.Request, error) {
	target, err := TargetURL(base, apiPath, requestPath)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create upstream request: %w", err)
	}
	connectionHeaders := connectionHeaderTokens(headers)
	usesAPIKey := ClientUsesAPIKeyHeader(headers)
	for key, values := range headers {
		if !ShouldForwardRequestHeader(key) || connectionHeaders[strings.ToLower(key)] {
			continue
		}
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	if len(body) > 0 && request.Header.Get("Content-Type") == "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if apiKey != "" {
		if usesAPIKey {
			request.Header.Set("x-api-key", apiKey)
		} else {
			if authPrefix == "" {
				authPrefix = "Bearer"
			}
			request.Header.Set("Authorization", authPrefix+" "+apiKey)
		}
	}
	return request, nil
}

func connectionHeaderTokens(headers http.Header) map[string]bool {
	result := map[string]bool{}
	for key, values := range headers {
		if !strings.EqualFold(key, "Connection") {
			continue
		}
		for _, value := range values {
			for _, token := range strings.Split(value, ",") {
				token = strings.TrimSpace(token)
				if token != "" {
					result[strings.ToLower(token)] = true
				}
			}
		}
	}
	return result
}
