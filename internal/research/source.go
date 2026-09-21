package research

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// FetchConfig controls source retrieval. The source policy never permits
// loopback, private, link-local, or metadata addresses.
type FetchConfig struct {
	// Client is an explicitly supplied, trusted transport boundary for tests or
	// callers that own the complete network policy. A nil Client gets the
	// package's SSRF-aware transport with ProxyFromEnvironment disabled.
	Client       *http.Client
	Timeout      time.Duration
	MaxBytes     int64
	MaxRedirects int
}

type SourceReader struct {
	config FetchConfig
}

func NewSourceReader(config FetchConfig) *SourceReader {
	if config.Timeout <= 0 {
		config.Timeout = DefaultSourceTimeout
	}
	if config.MaxBytes <= 0 {
		config.MaxBytes = DefaultMaxSourceBytes
	}
	if config.MaxRedirects <= 0 {
		config.MaxRedirects = DefaultMaxRedirects
	}
	return &SourceReader{config: config}
}

// Fetch reads an explicit HTTP(S) URL and retains the exact response bytes.
func (r *SourceReader) Fetch(ctx context.Context, rawURL string) (SourceSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	normalizedURL := strings.TrimSpace(rawURL)
	if err := validateSourceURL(ctx, normalizedURL); err != nil {
		return SourceSnapshot{}, err
	}
	requestContext, cancel := context.WithTimeout(ctx, r.config.Timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, normalizedURL, nil)
	if err != nil {
		return SourceSnapshot{}, fmt.Errorf("research: create source request: %w", err)
	}

	client := http.Client{}
	if r.config.Client != nil {
		client = *r.config.Client
	} else {
		client.Transport = sourceTransport()
	}
	if client.Timeout <= 0 {
		client.Timeout = r.config.Timeout
	}
	maxRedirects := r.config.MaxRedirects
	client.CheckRedirect = func(next *http.Request, history []*http.Request) error {
		if len(history) > maxRedirects {
			return fmt.Errorf("research: too many redirects")
		}
		if err := validateSourceURL(next.Context(), next.URL.String()); err != nil {
			return fmt.Errorf("research: unsafe redirect: %w", err)
		}
		return nil
	}
	response, err := client.Do(request)
	if err != nil {
		return SourceSnapshot{}, fmt.Errorf("research: fetch source: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return SourceSnapshot{}, fmt.Errorf("research: source returned HTTP %d", response.StatusCode)
	}
	limited := io.LimitReader(response.Body, r.config.MaxBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return SourceSnapshot{}, fmt.Errorf("research: read source: %w", err)
	}
	if int64(len(body)) > r.config.MaxBytes {
		return SourceSnapshot{}, ErrSourceTooLarge
	}
	return NewSourceSnapshot(normalizedURL, response.Header.Get("Content-Type"), body, time.Now().UTC()), nil
}

func (r *SourceReader) FetchFeed(ctx context.Context, rawURL string) (SourceSnapshot, []FeedEntry, error) {
	snapshot, err := r.Fetch(ctx, rawURL)
	if err != nil {
		return SourceSnapshot{}, nil, err
	}
	entries, err := ParseFeed(snapshot)
	if err != nil {
		return SourceSnapshot{}, nil, err
	}
	return snapshot, entries, nil
}

func validateSourceURL(ctx context.Context, rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("research: source URL must be an absolute HTTP(S) URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("research: source URL scheme %q is not allowed", parsed.Scheme)
	}
	if parsed.User != nil {
		return fmt.Errorf("research: source URL user info is not allowed")
	}
	return validateHost(ctx, parsed.Hostname(), false)
}

func validateHost(ctx context.Context, hostname string, allowLocal bool) error {
	if hostname == "" {
		return fmt.Errorf("research: empty host")
	}
	if ip := net.ParseIP(hostname); ip != nil {
		if !allowLocal && blockedIP(ip) {
			return fmt.Errorf("research: destination address is blocked by SSRF policy")
		}
		return nil
	}
	if allowLocal {
		return nil
	}
	lookupContext, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(lookupContext, "ip", hostname)
	if err != nil {
		return fmt.Errorf("research: cannot resolve destination host")
	}
	for _, ip := range ips {
		if blockedIP(ip) {
			return fmt.Errorf("research: destination address is blocked by SSRF policy")
		}
	}
	return nil
}

func blockedIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return true
	}
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	for _, network := range blockedNetworks {
		if network.Contains(v4) {
			return true
		}
	}
	return false
}

var blockedNetworks = mustCIDRs([]string{
	"169.254.0.0/16", // cloud metadata and link-local
	"100.64.0.0/10",  // shared address space
	"198.18.0.0/15",  // benchmarking network
	"192.0.0.0/24",
	"192.0.2.0/24",
	"198.51.100.0/24",
	"203.0.113.0/24",
})

func mustCIDRs(values []string) []*net.IPNet {
	networks := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			panic(err)
		}
		networks = append(networks, network)
	}
	return networks
}

// sourceTransport checks the address again at the actual dial point. This
// closes the DNS-rebinding gap between validateSourceURL and http.Transport.
func sourceTransport() *http.Transport {
	return &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if err != nil {
				return nil, fmt.Errorf("research: resolve source at dial time: %w", err)
			}
			for _, ip := range ips {
				if blockedIP(ip) {
					return nil, fmt.Errorf("research: destination address is blocked by SSRF policy")
				}
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("research: source host has no addresses")
			}
			return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
	}
}

// ExtractText is intentionally conservative: Original remains authoritative.
// It only exposes readable text for prompting and quote verification.
func ExtractText(contentType string, body []byte) string {
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	trimmed := strings.TrimSpace(string(body))
	if mediaType == "application/json" || mediaType == "text/json" || strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var value any
		if json.Unmarshal(body, &value) == nil {
			var parts []string
			collectJSONText(value, &parts)
			return strings.TrimSpace(strings.Join(parts, "\n"))
		}
	}
	if mediaType == "application/xml" || mediaType == "text/xml" || strings.Contains(mediaType, "rss") || strings.Contains(mediaType, "atom") || strings.HasPrefix(trimmed, "<?xml") || strings.HasPrefix(trimmed, "<rss") || strings.HasPrefix(trimmed, "<feed") {
		return xmlText(body)
	}
	if mediaType == "text/html" || strings.Contains(mediaType, "html") || strings.HasPrefix(trimmed, "<html") {
		return stripHTMLText(trimmed)
	}
	return strings.TrimSpace(string(body))
}

func collectJSONText(value any, parts *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		// JSON object traversal is ordered so extracted prompts and citations are
		// reproducible; recognized text fields follow their semantic priority.
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.SliceStable(keys, func(i, j int) bool {
			leftRank, rightRank := jsonTextKeyRank(keys[i]), jsonTextKeyRank(keys[j])
			if leftRank != rightRank {
				return leftRank < rightRank
			}
			leftKey, rightKey := strings.ToLower(keys[i]), strings.ToLower(keys[j])
			if leftKey != rightKey {
				return leftKey < rightKey
			}
			return keys[i] < keys[j]
		})
		for _, key := range keys {
			nested := typed[key]
			if stringValue, ok := nested.(string); ok && isTextKey(key) && strings.TrimSpace(stringValue) != "" {
				*parts = append(*parts, stringValue)
				continue
			}
			collectJSONText(nested, parts)
		}
	case []any:
		for _, nested := range typed {
			collectJSONText(nested, parts)
		}
	}
}

func jsonTextKeyRank(key string) int {
	switch strings.ToLower(key) {
	case "title":
		return 0
	case "headline":
		return 1
	case "description":
		return 2
	case "summary":
		return 3
	case "content":
		return 4
	case "text":
		return 5
	case "body":
		return 6
	case "abstract":
		return 7
	default:
		return 8
	}
}

func isTextKey(key string) bool {
	switch strings.ToLower(key) {
	case "title", "description", "summary", "content", "text", "body", "abstract", "headline":
		return true
	default:
		return false
	}
}

var (
	scriptStylePattern = regexp.MustCompile(`(?is)<(script|style)\b.*?</(script|style)\s*>`)
	tagPattern         = regexp.MustCompile(`(?s)<[^>]*>`)
)

func stripHTMLText(value string) string {
	value = scriptStylePattern.ReplaceAllString(value, " ")
	value = tagPattern.ReplaceAllString(value, " ")
	return strings.TrimSpace(html.UnescapeString(value))
}

func xmlText(body []byte) string {
	decoder := xml.NewDecoder(strings.NewReader(string(body)))
	var parts []string
	blockedDepth := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return strings.TrimSpace(string(body))
		}
		switch value := token.(type) {
		case xml.StartElement:
			if value.Name.Local == "script" || value.Name.Local == "style" {
				blockedDepth++
			}
		case xml.EndElement:
			if (value.Name.Local == "script" || value.Name.Local == "style") && blockedDepth > 0 {
				blockedDepth--
			}
		case xml.CharData:
			if blockedDepth == 0 && strings.TrimSpace(string(value)) != "" {
				parts = append(parts, strings.TrimSpace(string(value)))
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

type FeedEntry struct {
	Title     string `json:"title"`
	Link      string `json:"link"`
	Summary   string `json:"summary"`
	Published string `json:"published,omitempty"`
}

// ParseFeed parses only the common RSS and Atom fields while preserving the
// complete source in SourceSnapshot. Unknown extensions are ignored.
func ParseFeed(snapshot SourceSnapshot) ([]FeedEntry, error) {
	decoder := xml.NewDecoder(strings.NewReader(string(snapshot.Original)))
	var entries []FeedEntry
	var current *FeedEntry
	var currentField string
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("research: parse feed XML: %w", err)
		}
		switch value := token.(type) {
		case xml.StartElement:
			local := strings.ToLower(value.Name.Local)
			if local == "item" || local == "entry" {
				if current != nil {
					return nil, ErrUnsupportedFeed
				}
				current = &FeedEntry{}
				continue
			}
			if current == nil {
				continue
			}
			if local == "link" {
				for _, attr := range value.Attr {
					if attr.Name.Local == "href" {
						current.Link = strings.TrimSpace(attr.Value)
					}
				}
			}
			switch local {
			case "title", "description", "summary", "content", "encoded", "published", "updated", "pubdate":
				currentField = local
			default:
				currentField = ""
			}
		case xml.CharData:
			if current == nil || currentField == "" {
				continue
			}
			text := strings.TrimSpace(string(value))
			if text == "" {
				continue
			}
			switch currentField {
			case "title":
				current.Title += text
			case "description", "summary", "content", "encoded":
				current.Summary += text
			case "published", "updated", "pubdate":
				current.Published += text
			}
		case xml.EndElement:
			local := strings.ToLower(value.Name.Local)
			if local == "item" || local == "entry" {
				if current == nil || (current.Title == "" && current.Summary == "" && current.Link == "") {
					return nil, ErrUnsupportedFeed
				}
				entries = append(entries, *current)
				current = nil
			}
			currentField = ""
		}
	}
	if len(entries) == 0 {
		return nil, ErrUnsupportedFeed
	}
	return entries, nil
}
