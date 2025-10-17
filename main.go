package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	OAuthTokenEndpoint = "https://api.github.com/copilot_internal/v2/token"
	APIEndpoint        = "https://api.githubcopilot.com"
)

func init() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		AddSource: true,
	}))
	slog.SetDefault(logger)
}

type APIToken struct {
	ExpiresAt int64  `json:"expires_at"`
	RefreshIn int64  `json:"refresh_in"`
	Token     string `json:"token"`
}

type TokenSource struct {
	mu         sync.RWMutex
	apiToken   APIToken
	oauthToken string

	client *http.Client
}

func NewTokenSource(oauthToken string) *TokenSource {
	return &TokenSource{
		oauthToken: oauthToken,

		client: http.DefaultClient,
	}
}

func (ts *TokenSource) Start(ctx context.Context) {
	var timeout <-chan time.Time
	var retry <-chan time.Time

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	first := make(chan struct{})
	close(first)

	for {
		select {
		case <-ctx.Done():
			return
		case <-timeout:
			timeout = nil
		case <-retry:
			retry = nil
		case <-first:
			first = nil
		case <-ticker.C:
		}

		if ts.Ready() {
			continue
		}

		var apiToken APIToken
		if err := ts.refresh(ctx, &apiToken); err != nil {
			slog.Error("failed to refresh token", "error", err, "retry", 5*time.Second)
			retry = time.After(5 * time.Second)
			continue
		}
		slog.Info("token refreshed", "expires_at", time.Unix(apiToken.ExpiresAt, 0), "refresh_in", time.Duration(apiToken.RefreshIn)*time.Second)

		ts.mu.Lock()
		ts.apiToken = apiToken
		ts.mu.Unlock()

		timeout = time.After(time.Duration(apiToken.RefreshIn-10) * time.Second)
	}
}

func (ts *TokenSource) refresh(ctx context.Context, apiToken *APIToken) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, OAuthTokenEndpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+ts.oauthToken)
	req.Header.Set("User-Agent", "vscode-chat/dev")
	req.Header.Set("Accept", "application/json")

	rsp, err := ts.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to refresh token: %w", err)
	}
	defer rsp.Body.Close()

	data, err := io.ReadAll(rsp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if rsp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to refresh token: status: %d, body: %s", rsp.StatusCode, string(data))
	}

	if err = json.Unmarshal(data, apiToken); err != nil {
		return fmt.Errorf("failed to unmarshal token: %w", err)
	}

	return nil
}

func (ts *TokenSource) Ready() bool {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	return ts.ready()
}

func (ts *TokenSource) ready() bool {
	return ts.apiToken.ExpiresAt > time.Now().Unix()
}

func (ts *TokenSource) Token() string {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	return ts.apiToken.Token
}

func (ts *TokenSource) CustomHeaders(header http.Header) {
	header.Set("Authorization", "Bearer "+ts.Token())
	header.Set("User-Agent", "vscode-chat/dev")
	header.Set("Copilot-Integration-Id", "vscode-chat")
	header.Set("Editor-Version", "Neovim/0.11.0")
	header.Set("Editor-Plugin-Version", "copilot-chat/0.1.0")
}

func (ts *TokenSource) NewProxy(upstream *url.URL) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(upstream)
			ts.CustomHeaders(r.Out.Header)
		},
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ts.Ready() {
			http.Error(w, "Service not ready", http.StatusServiceUnavailable)
			return
		}
		tracker := TrackStatusCode(w)
		start := time.Now()

		defer func() {
			slog.Info("proxied request", "method", r.Method, "url", r.URL.String(), "duration", time.Since(start), "status", tracker.code, "name", "accesslog")
		}()

		proxy.ServeHTTP(tracker, r)
	})
}

var Args struct {
	OAuthToken  string
	AccessToken string
	Addr        string
	BasePath    string
}

func init() {
	flag.StringVar(&Args.OAuthToken, "oauth-token", "", "OAuth token for GitHub API")
	flag.StringVar(&Args.Addr, "addr", ":8080", "Address to listen on")
	flag.StringVar(&Args.AccessToken, "access-token", "", "Access token for OpenAI API")
	flag.StringVar(&Args.BasePath, "base-path", "/api/v1", "Base path for the API")
}

type Middleware func(http.Handler) http.Handler

func applyMiddlewares(handler http.Handler, middlewares ...Middleware) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		handler = middlewares[i](handler)
	}
	return handler
}

func stripPrefix(prefix string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.StripPrefix(prefix, next)
	}
}

func verifyAccessToken(token string) Middleware {
	return func(next http.Handler) http.Handler {
		if token == "" {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader != "Bearer "+token {
				http.Error(w, "Invalid access token", http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

type StatusCodeTracker struct {
	http.ResponseWriter

	code int
}

func TrackStatusCode(w http.ResponseWriter) *StatusCodeTracker {
	return &StatusCodeTracker{ResponseWriter: w, code: 0}
}

func (s *StatusCodeTracker) WriteHeader(code int) {
	if s.code != 0 {
		return
	}
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *StatusCodeTracker) Write(b []byte) (int, error) {
	if s.code == 0 {
		s.code = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *StatusCodeTracker) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}

func parseOAuthToken() (string, error) {
	apps := filepath.Join(os.Getenv("HOME"), ".config/github-copilot/apps.json")
	data, err := os.ReadFile(apps)
	if err != nil {
		return "", fmt.Errorf("failed to read apps.json: %w", err)
	}
	type TokenObject struct {
		User       string `json:"user"`
		OAuthToken string `json:"oauth_token"`
	}
	cfg := make(map[string]TokenObject)
	err = json.Unmarshal(data, &cfg)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal apps.json: %w", err)
	}
	for _, obj := range cfg {
		return obj.OAuthToken, nil
	}
	return "", fmt.Errorf("no OAuth token found in apps.json")
}

func main() {
	flag.Parse()

	if Args.AccessToken == "" {
		slog.Warn("access token is missing")
	}

	if Args.OAuthToken == "" {
		slog.Info("no OAuth token provided, trying to read from apps.json")

		oauthToken, err := parseOAuthToken()
		if err != nil {
			slog.Error("failed to read OAuth token from apps.json", "error", err)

			os.Exit(1)
		}

		Args.OAuthToken = oauthToken
	}

	ts := NewTokenSource(Args.OAuthToken)

	upstream, _ := url.Parse(APIEndpoint)
	proxy := ts.NewProxy(upstream)

	ctx := context.Background()
	go ts.Start(ctx)

	mux := http.NewServeMux()

	middlewares := []Middleware{
		stripPrefix(Args.BasePath),
		verifyAccessToken(Args.AccessToken),
	}
	apiHandler := applyMiddlewares(proxy, middlewares...)
	mux.Handle(Args.BasePath+"/", apiHandler)
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		if ts.Ready() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("OK"))
			return
		}
		http.Error(w, "Service not ready", http.StatusServiceUnavailable)
	})

	srv := &http.Server{
		Addr:              Args.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	if err := srv.ListenAndServe(); err != nil {
		panic(err)
	}
}
