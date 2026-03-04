package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/MRL-00/viaduct-ai/internal/connector"
)

const (
	azureManagementURL = "https://management.azure.com"
	azureScope         = "https://management.azure.com/.default"
)

type Connector struct {
	httpClient *http.Client

	tenantID       string
	clientID       string
	clientSecret   string
	subscriptionID string

	cred *azidentity.ClientSecretCredential

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
	return "azure"
}

func (c *Connector) Description() string {
	return "Reads Azure subscriptions, resources, alerts, and metrics"
}

func (c *Connector) Configure(cfg connector.ConnectorConfig) error {
	var ok bool
	if c.tenantID, ok = cfg["tenant_id"].(string); !ok || c.tenantID == "" {
		return fmt.Errorf("azure.tenant_id is required")
	}
	if c.clientID, ok = cfg["client_id"].(string); !ok || c.clientID == "" {
		return fmt.Errorf("azure.client_id is required")
	}
	if c.clientSecret, ok = cfg["client_secret"].(string); !ok || c.clientSecret == "" {
		return fmt.Errorf("azure.client_secret is required")
	}
	if c.subscriptionID, ok = cfg["subscription_id"].(string); !ok || c.subscriptionID == "" {
		return fmt.Errorf("azure.subscription_id is required")
	}

	cred, err := azidentity.NewClientSecretCredential(c.tenantID, c.clientID, c.clientSecret, nil)
	if err != nil {
		return fmt.Errorf("create azure credential: %w", err)
	}
	c.cred = cred
	return nil
}

func (c *Connector) HealthCheck(ctx context.Context) error {
	_, err := c.getAccessToken(ctx)
	if err != nil {
		return err
	}
	_, err = c.listResourceGroups(ctx)
	return err
}

func (c *Connector) List(ctx context.Context, query connector.Query) ([]connector.Resource, error) {
	resourceType := query.Filter["resource"]
	if resourceType == "" {
		resourceType = "resources"
	}

	switch resourceType {
	case "subscriptions":
		return c.listSubscriptions(ctx)
	case "resource_groups":
		return c.listResourceGroups(ctx)
	case "resources":
		return c.listResources(ctx)
	case "monitor_alerts":
		return c.listAlerts(ctx, query)
	case "monitor_metrics":
		return c.listMetrics(ctx, query)
	default:
		return nil, fmt.Errorf("unsupported azure list resource %q", resourceType)
	}
}

func (c *Connector) Read(ctx context.Context, id string) (connector.Resource, error) {
	if strings.HasPrefix(id, "/subscriptions/") {
		path := fmt.Sprintf("%s%s?api-version=2021-04-01", azureManagementURL, id)
		body, err := c.requestWithBackoff(ctx, http.MethodGet, path, "resource")
		if err != nil {
			return connector.Resource{}, err
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			return connector.Resource{}, fmt.Errorf("decode azure resource: %w", err)
		}
		return connector.Resource{
			ID:      id,
			Type:    "azure_resource",
			Name:    getString(payload, "name"),
			Content: "",
			Metadata: map[string]any{
				"raw": payload,
			},
		}, nil
	}
	return connector.Resource{}, fmt.Errorf("unsupported resource id %q", id)
}

func (c *Connector) Search(ctx context.Context, query string) ([]connector.Resource, error) {
	path := fmt.Sprintf("%s/subscriptions/%s/resources?$filter=%s&api-version=2021-04-01",
		azureManagementURL, url.PathEscape(c.subscriptionID),
		url.QueryEscape("substringof('"+query+"', name)"))
	body, err := c.requestWithBackoff(ctx, http.MethodGet, path, "resource_search")
	if err != nil {
		return nil, err
	}
	return decodeResourceList(body, "azure_resource"), nil
}

func (c *Connector) listSubscriptions(ctx context.Context) ([]connector.Resource, error) {
	path := fmt.Sprintf("%s/subscriptions?api-version=2020-01-01", azureManagementURL)
	body, err := c.requestWithBackoff(ctx, http.MethodGet, path, "subscriptions")
	if err != nil {
		return nil, err
	}
	return decodeResourceList(body, "azure_subscription"), nil
}

func (c *Connector) listResourceGroups(ctx context.Context) ([]connector.Resource, error) {
	client, err := armresources.NewResourceGroupsClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return nil, fmt.Errorf("create resource groups client: %w", err)
	}
	pager := client.NewListPager(nil)

	resources := make([]connector.Resource, 0)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list resource groups: %w", err)
		}
		for _, rg := range page.Value {
			resources = append(resources, connector.Resource{
				ID:      deref(rg.ID),
				Type:    "azure_resource_group",
				Name:    deref(rg.Name),
				Content: "",
				Metadata: map[string]any{
					"location": deref(rg.Location),
				},
			})
		}
	}
	return resources, nil
}

func (c *Connector) listResources(ctx context.Context) ([]connector.Resource, error) {
	client, err := armresources.NewClient(c.subscriptionID, c.cred, nil)
	if err != nil {
		return nil, fmt.Errorf("create resources client: %w", err)
	}
	pager := client.NewListPager(nil)

	resources := make([]connector.Resource, 0)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list resources: %w", err)
		}
		for _, r := range page.Value {
			resources = append(resources, connector.Resource{
				ID:      deref(r.ID),
				Type:    deref(r.Type),
				Name:    deref(r.Name),
				Content: "",
				Metadata: map[string]any{
					"location":       deref(r.Location),
					"resource_group": extractResourceGroup(deref(r.ID)),
				},
			})
		}
	}
	return resources, nil
}

func (c *Connector) listAlerts(ctx context.Context, query connector.Query) ([]connector.Resource, error) {
	severity := query.Filter["severity"]
	if severity == "" {
		severity = "2"
	}
	timeRange := query.Filter["time_range"]
	if timeRange == "" {
		timeRange = "PT12H"
	}
	path := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.AlertsManagement/alerts?api-version=2023-07-12-preview&$filter=%s",
		azureManagementURL,
		url.PathEscape(c.subscriptionID),
		url.QueryEscape(fmt.Sprintf("severity ge %s and monitorCondition eq 'Fired'", severity)),
	)

	body, err := c.requestWithBackoff(ctx, http.MethodGet, path, "alerts")
	if err != nil {
		return nil, err
	}
	resources := decodeResourceList(body, "azure_alert")
	for i := range resources {
		if resources[i].Metadata == nil {
			resources[i].Metadata = map[string]any{}
		}
		resources[i].Metadata["time_range"] = timeRange
	}
	return resources, nil
}

func (c *Connector) listMetrics(ctx context.Context, query connector.Query) ([]connector.Resource, error) {
	resourceID := query.Filter["resource_id"]
	metricName := query.Filter["metric"]
	if resourceID == "" || metricName == "" {
		return nil, fmt.Errorf("resource_id and metric are required for monitor_metrics")
	}
	timeSpan := query.Filter["timespan"]
	if timeSpan == "" {
		end := time.Now().UTC()
		start := end.Add(-1 * time.Hour)
		timeSpan = start.Format(time.RFC3339) + "/" + end.Format(time.RFC3339)
	}

	path := fmt.Sprintf("%s%s/providers/microsoft.insights/metrics?api-version=2023-10-01&metricnames=%s&timespan=%s",
		azureManagementURL,
		resourceID,
		url.QueryEscape(metricName),
		url.QueryEscape(timeSpan),
	)
	body, err := c.requestWithBackoff(ctx, http.MethodGet, path, "metrics")
	if err != nil {
		return nil, err
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode metrics: %w", err)
	}

	return []connector.Resource{{
		ID:      resourceID + ":metrics:" + metricName,
		Type:    "azure_metric",
		Name:    metricName,
		Content: "metric payload",
		Metadata: map[string]any{
			"resource_id": resourceID,
			"timespan":    timeSpan,
			"data":        payload,
		},
	}}, nil
}

func (c *Connector) requestWithBackoff(ctx context.Context, method, fullURL, endpoint string) ([]byte, error) {
	token, err := c.getAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	backoff := 500 * time.Millisecond
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, fullURL, nil)
		if err != nil {
			return nil, fmt.Errorf("build azure request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
		} else {
			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr != nil {
				return nil, fmt.Errorf("read azure response: %w", readErr)
			}
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return body, nil
			}
			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
				lastErr = fmt.Errorf("azure %s temporary failure (%d): %s",
					endpoint, resp.StatusCode, string(body))
			} else if resp.StatusCode == http.StatusUnauthorized {
				c.invalidateToken()
				token, err = c.getAccessToken(ctx)
				if err != nil {
					return nil, err
				}
				lastErr = fmt.Errorf("azure %s unauthorized", endpoint)
			} else {
				return nil, fmt.Errorf("azure %s failed (%d): %s", endpoint, resp.StatusCode, string(body))
			}
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
			backoff *= 2
		}
	}

	return nil, fmt.Errorf("azure %s failed after retries: %w", endpoint, lastErr)
}

func (c *Connector) getAccessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.accessToken != "" && time.Now().Before(c.expiresAt.Add(-30*time.Second)) {
		return c.accessToken, nil
	}

	tk, err := c.cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{azureScope}})
	if err != nil {
		return "", fmt.Errorf("get azure token: %w", err)
	}
	c.accessToken = tk.Token
	c.expiresAt = tk.ExpiresOn
	return c.accessToken, nil
}

func (c *Connector) invalidateToken() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.accessToken = ""
	c.expiresAt = time.Time{}
}

func decodeResourceList(body []byte, defaultType string) []connector.Resource {
	var response struct {
		Value []map[string]any `json:"value"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil
	}
	resources := make([]connector.Resource, 0, len(response.Value))
	for _, item := range response.Value {
		typeName := getString(item, "type")
		if typeName == "" {
			typeName = defaultType
		}
		resources = append(resources, connector.Resource{
			ID:      getString(item, "id"),
			Type:    typeName,
			Name:    getString(item, "name"),
			Content: "",
			Metadata: map[string]any{
				"raw": item,
			},
		})
	}
	return resources
}

func getString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if ok {
		return s
	}
	return fmt.Sprint(v)
}

func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func extractResourceGroup(resourceID string) string {
	parts := strings.Split(resourceID, "/")
	for i := 0; i < len(parts)-1; i++ {
		if strings.EqualFold(parts[i], "resourceGroups") {
			return parts[i+1]
		}
	}
	return ""
}

func parseInt(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	i, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return i
}

var (
	_ connector.Connector = (*Connector)(nil)
	_ connector.Reader    = (*Connector)(nil)
)
