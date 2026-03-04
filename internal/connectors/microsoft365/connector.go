package microsoft365

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/MRL-00/viaduct-ai/internal/connector"
)

const (
	graphBaseURL = "https://graph.microsoft.com"
	tokenScope   = "https://graph.microsoft.com/.default"
)

type Connector struct {
	httpClient *http.Client

	tenantID     string
	clientID     string
	clientSecret string

	mu          sync.Mutex
	accessToken string
	expiresAt   time.Time
}

func New() *Connector {
	return &Connector{
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Connector) Name() string {
	return "microsoft365"
}

func (c *Connector) Description() string {
	return "Reads Teams and SharePoint resources through Microsoft Graph API"
}

func (c *Connector) Configure(cfg connector.ConnectorConfig) error {
	var ok bool
	if c.tenantID, ok = asString(cfg["tenant_id"]); !ok || c.tenantID == "" {
		return fmt.Errorf("microsoft365.tenant_id is required")
	}
	if c.clientID, ok = asString(cfg["client_id"]); !ok || c.clientID == "" {
		return fmt.Errorf("microsoft365.client_id is required")
	}
	if c.clientSecret, ok = asString(cfg["client_secret"]); !ok || c.clientSecret == "" {
		return fmt.Errorf("microsoft365.client_secret is required")
	}
	return nil
}

func (c *Connector) HealthCheck(ctx context.Context) error {
	_, err := c.graphRequest(ctx, http.MethodGet, "/v1.0/organization?$top=1", nil, "organization")
	return err
}

func (c *Connector) List(ctx context.Context, query connector.Query) ([]connector.Resource, error) {
	resourceType := query.Filter["resource"]
	if resourceType == "" {
		resourceType = "sharepoint_sites"
	}

	switch resourceType {
	case "sharepoint_sites":
		return c.listSharePointSites(ctx, query)
	case "sharepoint_files":
		return c.listSharePointFiles(ctx, query)
	case "teams_channels":
		return c.listTeamsChannels(ctx, query)
	case "teams_messages":
		return c.listTeamsMessages(ctx, query)
	default:
		return nil, fmt.Errorf("unsupported microsoft365 list resource %q", resourceType)
	}
}

func (c *Connector) Read(ctx context.Context, id string) (connector.Resource, error) {
	parts := strings.Split(id, ":")
	if len(parts) == 0 {
		return connector.Resource{}, fmt.Errorf("invalid resource id")
	}

	switch parts[0] {
	case "driveItem":
		if len(parts) < 3 {
			return connector.Resource{}, fmt.Errorf(
				"driveItem id must be driveItem:<driveID>:<itemID>")
		}
		return c.readDriveItem(ctx, parts[1], parts[2])
	case "teamsMessage":
		if len(parts) < 4 {
			return connector.Resource{}, fmt.Errorf(
				"teamsMessage id must be teamsMessage:<teamID>:<channelID>:<messageID>")
		}
		return c.readTeamsMessage(ctx, parts[1], parts[2], parts[3])
	default:
		return connector.Resource{}, fmt.Errorf("unsupported resource id prefix %q", parts[0])
	}
}

func (c *Connector) Search(ctx context.Context, query string) ([]connector.Resource, error) {
	payload := map[string]any{
		"requests": []map[string]any{
			{
				"entityTypes": []string{"driveItem", "chatMessage"},
				"query": map[string]string{
					"queryString": query,
				},
				"from": 0,
				"size": 25,
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal graph search payload: %w", err)
	}

	resBody, err := c.graphRequest(ctx, http.MethodPost, "/v1.0/search/query", bytes.NewReader(body), "search")
	if err != nil {
		return nil, err
	}

	var response struct {
		Value []struct {
			HitsContainers []struct {
				Hits []struct {
					Resource map[string]any `json:"resource"`
				} `json:"hits"`
			} `json:"hitsContainers"`
		} `json:"value"`
	}
	if err := json.Unmarshal(resBody, &response); err != nil {
		return nil, fmt.Errorf("decode graph search response: %w", err)
	}

	resources := make([]connector.Resource, 0)
	for _, containerGroup := range response.Value {
		for _, container := range containerGroup.HitsContainers {
			for _, hit := range container.Hits {
				id, _ := asString(hit.Resource["id"])
				name, _ := asString(hit.Resource["name"])
				webURL, _ := asString(hit.Resource["webUrl"])
				resources = append(resources, connector.Resource{
					ID:      id,
					Type:    "search_result",
					Name:    name,
					Content: "",
					Metadata: map[string]any{
						"web_url": webURL,
					},
				})
			}
		}
	}

	return resources, nil
}

func (c *Connector) listSharePointSites(ctx context.Context, query connector.Query) ([]connector.Resource, error) {
	path := "/v1.0/sites?search=*"
	if q := query.Filter["query"]; q != "" {
		path = "/v1.0/sites?search=" + url.QueryEscape(q)
	}
	body, err := c.graphRequest(ctx, http.MethodGet, path, nil, "sites")
	if err != nil {
		return nil, err
	}
	var response struct {
		Value []struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
			WebURL      string `json:"webUrl"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decode sites: %w", err)
	}
	resources := make([]connector.Resource, 0, len(response.Value))
	for _, site := range response.Value {
		resources = append(resources, connector.Resource{
			ID:      site.ID,
			Type:    "sharepoint_site",
			Name:    site.DisplayName,
			Content: site.WebURL,
			Metadata: map[string]any{
				"web_url": site.WebURL,
			},
		})
	}
	return resources, nil
}

func (c *Connector) listSharePointFiles(ctx context.Context, query connector.Query) ([]connector.Resource, error) {
	driveID := query.Filter["drive_id"]
	if driveID == "" {
		return nil, errors.New("drive_id is required for sharepoint_files")
	}
	path := fmt.Sprintf("/v1.0/drives/%s/root/children", url.PathEscape(driveID))
	if query.Limit > 0 {
		path += fmt.Sprintf("?$top=%d", query.Limit)
	}
	body, err := c.graphRequest(ctx, http.MethodGet, path, nil, "drive_items")
	if err != nil {
		return nil, err
	}
	var response struct {
		Value []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			WebURL string `json:"webUrl"`
			File   struct {
				MimeType string `json:"mimeType"`
			} `json:"file"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decode drive items: %w", err)
	}

	resources := make([]connector.Resource, 0, len(response.Value))
	for _, item := range response.Value {
		resources = append(resources, connector.Resource{
			ID:      fmt.Sprintf("driveItem:%s:%s", driveID, item.ID),
			Type:    "sharepoint_file",
			Name:    item.Name,
			Content: "",
			Metadata: map[string]any{
				"mime_type": item.File.MimeType,
				"web_url":   item.WebURL,
			},
		})
	}
	return resources, nil
}

func (c *Connector) listTeamsChannels(ctx context.Context, query connector.Query) ([]connector.Resource, error) {
	teamID := query.Filter["team_id"]
	if teamID == "" {
		return nil, errors.New("team_id is required for teams_channels")
	}
	path := fmt.Sprintf("/v1.0/teams/%s/channels", url.PathEscape(teamID))
	body, err := c.graphRequest(ctx, http.MethodGet, path, nil, "teams_channels")
	if err != nil {
		return nil, err
	}
	var response struct {
		Value []struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
			Description string `json:"description"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decode teams channels: %w", err)
	}

	resources := make([]connector.Resource, 0, len(response.Value))
	for _, channel := range response.Value {
		resources = append(resources, connector.Resource{
			ID:      channel.ID,
			Type:    "teams_channel",
			Name:    channel.DisplayName,
			Content: channel.Description,
			Metadata: map[string]any{
				"team_id": teamID,
			},
		})
	}
	return resources, nil
}

func (c *Connector) listTeamsMessages(ctx context.Context, query connector.Query) ([]connector.Resource, error) {
	teamID := query.Filter["team_id"]
	channelID := query.Filter["channel_id"]
	if teamID == "" || channelID == "" {
		return nil, errors.New("team_id and channel_id are required for teams_messages")
	}
	top := 20
	if query.Limit > 0 {
		top = query.Limit
	}
	path := fmt.Sprintf("/v1.0/teams/%s/channels/%s/messages?$top=%d", url.PathEscape(teamID), url.PathEscape(channelID), top)
	body, err := c.graphRequest(ctx, http.MethodGet, path, nil, "teams_messages")
	if err != nil {
		return nil, err
	}
	var response struct {
		Value []struct {
			ID        string `json:"id"`
			CreatedAt string `json:"createdDateTime"`
			Body      struct {
				Content string `json:"content"`
			} `json:"body"`
			From struct {
				User struct {
					DisplayName string `json:"displayName"`
				} `json:"user"`
			} `json:"from"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decode teams messages: %w", err)
	}

	resources := make([]connector.Resource, 0, len(response.Value))
	for _, msg := range response.Value {
		resources = append(resources, connector.Resource{
			ID:      fmt.Sprintf("teamsMessage:%s:%s:%s", teamID, channelID, msg.ID),
			Type:    "teams_message",
			Name:    msg.From.User.DisplayName,
			Content: stripHTML(msg.Body.Content),
			Metadata: map[string]any{
				"created_at": msg.CreatedAt,
				"team_id":    teamID,
				"channel_id": channelID,
			},
		})
	}
	return resources, nil
}

func (c *Connector) readDriveItem(ctx context.Context, driveID, itemID string) (connector.Resource, error) {
	metaPath := fmt.Sprintf("/v1.0/drives/%s/items/%s", url.PathEscape(driveID), url.PathEscape(itemID))
	metaBody, err := c.graphRequest(ctx, http.MethodGet, metaPath, nil, "drive_item_meta")
	if err != nil {
		return connector.Resource{}, err
	}
	var meta struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		WebURL string `json:"webUrl"`
		File   struct {
			MimeType string `json:"mimeType"`
		} `json:"file"`
	}
	if err := json.Unmarshal(metaBody, &meta); err != nil {
		return connector.Resource{}, fmt.Errorf("decode drive item metadata: %w", err)
	}

	contentPath := fmt.Sprintf("/v1.0/drives/%s/items/%s/content", url.PathEscape(driveID), url.PathEscape(itemID))
	content, err := c.graphRequest(ctx, http.MethodGet, contentPath, nil, "drive_item_content")
	if err != nil {
		return connector.Resource{}, err
	}

	text := extractText(meta.Name, meta.File.MimeType, content)
	return connector.Resource{
		ID:      fmt.Sprintf("driveItem:%s:%s", driveID, itemID),
		Type:    "sharepoint_file",
		Name:    meta.Name,
		Content: text,
		Metadata: map[string]any{
			"web_url":   meta.WebURL,
			"mime_type": meta.File.MimeType,
		},
	}, nil
}

func (c *Connector) readTeamsMessage(ctx context.Context, teamID, channelID, messageID string) (connector.Resource, error) {
	path := fmt.Sprintf("/v1.0/teams/%s/channels/%s/messages/%s", url.PathEscape(teamID),
		url.PathEscape(channelID), url.PathEscape(messageID))
	body, err := c.graphRequest(ctx, http.MethodGet, path, nil, "teams_message")
	if err != nil {
		return connector.Resource{}, err
	}
	var msg struct {
		ID        string `json:"id"`
		CreatedAt string `json:"createdDateTime"`
		Body      struct {
			Content string `json:"content"`
		} `json:"body"`
		From struct {
			User struct {
				DisplayName string `json:"displayName"`
			} `json:"user"`
		} `json:"from"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		return connector.Resource{}, fmt.Errorf("decode teams message: %w", err)
	}

	return connector.Resource{
		ID:      fmt.Sprintf("teamsMessage:%s:%s:%s", teamID, channelID, messageID),
		Type:    "teams_message",
		Name:    msg.From.User.DisplayName,
		Content: stripHTML(msg.Body.Content),
		Metadata: map[string]any{
			"created_at": msg.CreatedAt,
			"team_id":    teamID,
			"channel_id": channelID,
		},
	}, nil
}

func (c *Connector) graphRequest(ctx context.Context, method, path string, body io.Reader, endpoint string) ([]byte, error) {
	if err := c.ensureToken(ctx); err != nil {
		return nil, err
	}

	fullURL := graphBaseURL + path
	var lastErr error
	backoff := 500 * time.Millisecond

	for attempt := 0; attempt < 5; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
		if err != nil {
			return nil, fmt.Errorf("build graph request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.accessToken)
		if method == http.MethodPost || method == http.MethodPatch {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
		} else {
			respBody, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr != nil {
				return nil, fmt.Errorf("read graph response: %w", readErr)
			}

			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return respBody, nil
			}

			if resp.StatusCode == http.StatusUnauthorized {
				c.invalidateToken()
				if err := c.ensureToken(ctx); err != nil {
					return nil, err
				}
				lastErr = fmt.Errorf("graph %s unauthorized", endpoint)
			} else if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
				lastErr = fmt.Errorf("graph %s temporary failure (%d): %s",
					endpoint, resp.StatusCode, string(respBody))
			} else {
				return nil, fmt.Errorf("graph %s request failed (%d): %s", endpoint, resp.StatusCode, string(respBody))
			}
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
			backoff *= 2
		}
	}

	return nil, fmt.Errorf("graph %s request failed after retries: %w", endpoint, lastErr)
}

func (c *Connector) ensureToken(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.accessToken != "" && time.Now().Before(c.expiresAt.Add(-30*time.Second)) {
		return nil
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", c.clientID)
	form.Set("client_secret", c.clientSecret)
	form.Set("scope", tokenScope)

	tokenURL := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", c.tenantID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request token: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("token request failed (%d): %s", resp.StatusCode, string(body))
	}

	var tokenResponse struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResponse); err != nil {
		return fmt.Errorf("decode token response: %w", err)
	}
	if tokenResponse.AccessToken == "" {
		return errors.New("empty graph access token")
	}

	c.accessToken = tokenResponse.AccessToken
	c.expiresAt = time.Now().Add(time.Duration(tokenResponse.ExpiresIn) * time.Second)
	return nil
}

func (c *Connector) invalidateToken() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.accessToken = ""
	c.expiresAt = time.Time{}
}

func extractText(fileName, mimeType string, content []byte) string {
	ext := strings.ToLower(fileName)
	if strings.HasSuffix(ext, ".docx") || mimeType == "application/vnd.openxmlformats-officedocument.wordprocessingml.document" {
		if text, err := parseDOCX(content); err == nil {
			return text
		}
	}
	if strings.HasSuffix(ext, ".xlsx") || mimeType == "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		if text, err := parseXLSX(content); err == nil {
			return text
		}
	}
	if strings.HasSuffix(ext, ".pdf") || mimeType == "application/pdf" {
		return parsePDF(content)
	}
	if isMostlyText(content) {
		return string(content)
	}
	return "[binary document extracted; textual parser unavailable for this format]"
}

func parseDOCX(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	for _, f := range zr.File {
		if f.Name != "word/document.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		b, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return "", err
		}
		return normalizeXMLText(string(b)), nil
	}
	return "", fmt.Errorf("word/document.xml not found")
}

func parseXLSX(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	sharedStrings := ""
	for _, f := range zr.File {
		if f.Name != "xl/sharedStrings.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		b, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return "", err
		}
		sharedStrings = normalizeXMLText(string(b))
		break
	}
	if sharedStrings != "" {
		return sharedStrings, nil
	}
	return "", fmt.Errorf("xl/sharedStrings.xml not found")
}

func parsePDF(data []byte) string {
	// Lightweight parser for scaffold: extracts printable runs from PDF streams.
	re := regexp.MustCompile(`[\x20-\x7E]{4,}`)
	matches := re.FindAllString(string(data), -1)
	if len(matches) == 0 {
		return ""
	}
	if len(matches) > 200 {
		matches = matches[:200]
	}
	return strings.Join(matches, "\n")
}

func normalizeXMLText(raw string) string {
	re := regexp.MustCompile(`<[^>]+>`)
	text := re.ReplaceAllString(raw, " ")
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\t", " ")
	text = strings.Join(strings.Fields(text), " ")
	return strings.TrimSpace(text)
}

func stripHTML(input string) string {
	re := regexp.MustCompile(`<[^>]+>`)
	clean := re.ReplaceAllString(input, " ")
	clean = strings.Join(strings.Fields(clean), " ")
	return strings.TrimSpace(clean)
}

func isMostlyText(data []byte) bool {
	if len(data) == 0 {
		return true
	}
	max := len(data)
	if max > 2048 {
		max = 2048
	}
	printable := 0
	for _, b := range data[:max] {
		if b == '\n' || b == '\r' || b == '\t' || (b >= 32 && b <= 126) {
			printable++
		}
	}
	return float64(printable)/float64(max) > 0.9
}

func asString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

var (
	_ connector.Connector = (*Connector)(nil)
	_ connector.Reader    = (*Connector)(nil)
)
