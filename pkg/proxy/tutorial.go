package proxy

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/eformat/rhai-workshop-plugin/pkg/api"
	"github.com/gorilla/mux"
)

// SubstitutionRule maps a default placeholder value to a real per-user value.
type SubstitutionRule struct {
	DefaultVal string
	RealVal    string
}

// SubstitutionStore is a thread-safe store of per-GUID substitution rules.
type SubstitutionStore struct {
	mu    sync.RWMutex
	rules map[string][]SubstitutionRule // guid -> rules
}

func NewSubstitutionStore() *SubstitutionStore {
	return &SubstitutionStore{
		rules: make(map[string][]SubstitutionRule),
	}
}

func (s *SubstitutionStore) SetRules(guid string, rules []SubstitutionRule) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules[guid] = rules
}

func (s *SubstitutionStore) SetAllRules(allRules map[string][]SubstitutionRule) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = allRules
}

func (s *SubstitutionStore) GetRules(guid string) []SubstitutionRule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rules[guid]
}

func (s *SubstitutionStore) ListGUIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	guids := make([]string, 0, len(s.rules))
	for k := range s.rules {
		guids = append(guids, k)
	}
	sort.Strings(guids)
	return guids
}

const clipboardPolyfill = `<script>(function(){if(!navigator.clipboard)return;var o=navigator.clipboard.writeText.bind(navigator.clipboard);navigator.clipboard.writeText=function(t){return o(t).catch(function(){var a=document.createElement("textarea");a.value=t;a.style.position="fixed";a.style.left="-9999px";document.body.appendChild(a);a.select();document.execCommand("copy");document.body.removeChild(a);return Promise.resolve();});};})();</script></head>`

// substitutableType returns true if the Content-Type should have string substitutions applied.
func substitutableType(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.Contains(ct, "text/html") ||
		strings.Contains(ct, "text/css") ||
		strings.Contains(ct, "application/javascript") ||
		strings.Contains(ct, "text/javascript")
}

// TutorialProxyHandler creates an HTTP handler that reverse-proxies tutorial
// requests to the upstream host, applying per-user string substitutions
// (replacing nginx sub_filter functionality).
func TutorialProxyHandler(store *SubstitutionStore, host string) http.HandlerFunc {
	target, _ := url.Parse("https://" + host)

	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		guid := vars["guid"]
		proxyPath := "/" + vars["path"]

		// Auth check: verify requesting user's GUID matches the proxy path
		user := api.GetUser(r)
		if user.Username == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		// Extract GUID from username (format: "user-<guid>")
		expectedPrefix := "user-"
		if strings.HasPrefix(user.Username, expectedPrefix) {
			userGuid := strings.TrimPrefix(user.Username, expectedPrefix)
			if userGuid != guid && !user.IsAdmin {
				http.Error(w, "forbidden: you can only access your own proxy", http.StatusForbidden)
				return
			}
		} else if !user.IsAdmin {
			// Non-admin users without "user-" prefix cannot access proxy
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		rules := store.GetRules(guid)

		proxy := &httputil.ReverseProxy{
			Director: func(req *http.Request) {
				req.URL.Scheme = target.Scheme
				req.URL.Host = target.Host
				req.Host = target.Host
				req.URL.Path = proxyPath
				req.URL.RawQuery = r.URL.RawQuery
				// Don't forward auth headers to upstream
				req.Header.Del("Authorization")
				// Request uncompressed to simplify substitution
				req.Header.Set("Accept-Encoding", "")
			},
			ModifyResponse: func(resp *http.Response) error {
				// Set Permissions-Policy for clipboard access in iframes
				resp.Header.Del("Permissions-Policy")
				resp.Header.Set("Permissions-Policy", "clipboard-read=*, clipboard-write=*")

				ct := resp.Header.Get("Content-Type")
				if !substitutableType(ct) || (len(rules) == 0 && !strings.Contains(ct, "text/html")) {
					return nil
				}

				// Read and potentially decompress the body
				var reader io.ReadCloser
				var err error
				switch resp.Header.Get("Content-Encoding") {
				case "gzip":
					reader, err = gzip.NewReader(resp.Body)
					if err != nil {
						return fmt.Errorf("gzip decompress: %w", err)
					}
					defer reader.Close()
					resp.Header.Del("Content-Encoding")
				default:
					reader = resp.Body
				}

				body, err := io.ReadAll(reader)
				if err != nil {
					return fmt.Errorf("reading body: %w", err)
				}
				resp.Body.Close()

				content := string(body)

				// Apply substitutions (rules are already sorted longest-first)
				for _, rule := range rules {
					content = strings.ReplaceAll(content, rule.DefaultVal, rule.RealVal)
				}

				// Inject clipboard polyfill into HTML pages
				if strings.Contains(ct, "text/html") {
					content = strings.Replace(content, "</head>", clipboardPolyfill, 1)
				}

				modified := []byte(content)
				resp.Body = io.NopCloser(bytes.NewReader(modified))
				resp.ContentLength = int64(len(modified))
				resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(modified)))

				return nil
			},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				log.Printf("Tutorial proxy error for %s: %v", r.URL.Path, err)
				http.Error(w, "proxy error", http.StatusBadGateway)
			},
		}

		proxy.ServeHTTP(w, r)
	}
}
