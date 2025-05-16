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

type APIToken struct {
	ExpiresAt int64  `json:"expires_at"`
	RefreshIn int64  `json:"refresh_in"`
	Token     string `json:"token"`
	Endpoints struct {
		API   string `json:"api"`
		Proxy string `json:"proxy"`
	} `json:"endpoints"`
}

type TokenSource struct {
	mu         sync.Mutex
	apiToken   APIToken
	oauthToken string
}

func NewTokenSource(oauthToken string) *TokenSource {
	return &TokenSource{
		oauthToken: oauthToken,
	}
}

func (ts *TokenSource) start(ctx context.Context) {
	var timeout <-chan time.Time
	var retry <-chan time.Time

	first := make(chan struct{})
	close(first)

	for {
		var err error
		select {
		case <-ctx.Done():
			return
		case <-timeout:
			timeout = nil
		case <-retry:
			retry = nil
		case <-first:
			first = nil
		}

		next, err := ts.refresh(ctx)
		if err != nil {
			slog.Error("failed to refresh token", "error", err, "retry", 5*time.Second)
			retry = time.After(5 * time.Second)
			continue
		}
		slog.Info("refreshed token")

		timeout = next
	}
}

func (ts *TokenSource) refresh(ctx context.Context) (<-chan time.Time, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/copilot_internal/v2/token", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+ts.oauthToken)
	req.Header.Set("User-Agent", "vscode-chat/dev")
	req.Header.Set("Accept", "application/json")

	rsp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh token: %w", err)
	}
	defer rsp.Body.Close()

	data, err := io.ReadAll(rsp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if rsp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to refresh token: status: %d, body: %s", rsp.StatusCode, string(data))
	}

	var token APIToken
	if err = json.Unmarshal(data, &token); err != nil {
		return nil, fmt.Errorf("failed to unmarshal token: %w", err)
	}

	timeout := time.After(time.Duration(token.RefreshIn-10) * time.Second)

	ts.apiToken = token

	return timeout, nil
}

func (ts *TokenSource) Ready() bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	return ts.ready()
}

func (ts *TokenSource) ready() bool {
	return ts.apiToken.ExpiresAt > time.Now().Unix()
}

func (ts *TokenSource) Endpoint() string {
	// ts.mu.Lock()
	// defer ts.mu.Unlock()

	// if endpoint := ts.apiToken.Endpoints.Proxy; endpoint != "" {
	// 	return endpoint
	// }
	// return ts.apiToken.Endpoints.API
	return "https://api.githubcopilot.com"
}

func (ts *TokenSource) Token() string {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	return ts.apiToken.Token
}

func (ts *TokenSource) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !ts.Ready() {
		http.Error(w, "Service not ready", http.StatusServiceUnavailable)
		return
	}

	slog.Info("proxy", "path", r.URL.Path)

	endpoint := ts.Endpoint()
	upstream, err := url.Parse(endpoint)
	if err != nil {
		http.Error(w, "Invalid upstream URL", http.StatusInternalServerError)
		return
	}
	token := ts.Token()

	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(upstream)
			r.Out.Header.Set("Authorization", "Bearer "+token)
			r.Out.Header.Set("User-Agent", "vscode-chat/dev")
			r.Out.Header.Set("Copilot-Integration-Id", "vscode-chat")
			r.Out.Header.Set("Editor-Version", "Neovim/0.11.0")
			r.Out.Header.Set("Editor-Plugin-Version", "copilot-chat/0.1.0")
		},
	}
	proxy.ServeHTTP(w, r)
}

var Args struct {
	OAuthToken  string
	AccessToken string
	Addr        string
}

func init() {
	flag.StringVar(&Args.OAuthToken, "oauth-token", "", "OAuth token for GitHub API")
	flag.StringVar(&Args.Addr, "addr", ":8080", "Address to listen on")
	flag.StringVar(&Args.AccessToken, "access-token", "", "Access token for OpenAI API")
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

	source := NewTokenSource(Args.OAuthToken)

	ctx := context.Background()
	go source.start(ctx)

	mux := http.NewServeMux()

	middlewares := []Middleware{
		stripPrefix("/api"),
		verifyAccessToken(Args.AccessToken),
	}
	apiHandler := applyMiddlewares(source, middlewares...)
	mux.Handle("/api/", apiHandler)
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		if source.Ready() {
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
