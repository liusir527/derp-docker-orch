package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"derp-orch/internal/docker"
	"derp-orch/internal/store"
)

// Client 控制面调用 regiond 的客户端,实现 docker.Orchestrator + HostInfoer。
type Client struct {
	baseURL string
	token   string
	hc      *http.Client
}

// NewClient 创建代理客户端。baseURL 如 https://agent.cd.easylan.site。
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		hc:      &http.Client{Timeout: 120 * time.Second},
	}
}

// EnsureNode 远程确保容器存在,并把宿主 IP 回写到实例。
func (c *Client) EnsureNode(ctx context.Context, i *store.Instance) error {
	var resp NodeResponse
	if err := c.do(ctx, http.MethodPost, "/v1/nodes", EnsureNodeRequest{Instance: i}, &resp); err != nil {
		return err
	}
	if resp.HostIP != "" {
		i.HostIP = resp.HostIP
	}
	return nil
}

// RemoveNode 远程删除容器(幂等)。
func (c *Client) RemoveNode(ctx context.Context, i *store.Instance) error {
	return c.do(ctx, http.MethodDelete, "/v1/nodes/"+url.PathEscape(i.ContainerName), nil, nil)
}

// NodeState 查询远程容器状态。
func (c *Client) NodeState(ctx context.Context, i *store.Instance) (docker.ContainerState, error) {
	var resp NodeResponse
	if err := c.do(ctx, http.MethodGet, "/v1/nodes/"+url.PathEscape(i.ContainerName), nil, &resp); err != nil {
		return docker.StateMissing, err
	}
	return resp.State, nil
}

// Stats 远程容器资源统计。
func (c *Client) Stats(ctx context.Context) (map[string]*docker.ContainerStat, error) {
	var resp StatsResponse
	if err := c.do(ctx, http.MethodGet, "/v1/stats", nil, &resp); err != nil {
		return nil, err
	}
	if resp.Stats == nil {
		return map[string]*docker.ContainerStat{}, nil
	}
	return resp.Stats, nil
}

// HostInfo 远程宿主资源概况。
func (c *Client) HostInfo(ctx context.Context) (docker.HostInfo, error) {
	var resp InfoResponse
	if err := c.do(ctx, http.MethodGet, "/v1/info", nil, &resp); err != nil {
		return docker.HostInfo{}, err
	}
	return resp.HostInfo, nil
}

// Info 探测代理连通性与资源(管理面板"测试"按钮)。
func (c *Client) Info(ctx context.Context) (*InfoResponse, error) {
	var resp InfoResponse
	if err := c.do(ctx, http.MethodGet, "/v1/info", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("序列化请求: %w", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("构造 regiond 请求: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("连接 regiond(%s) 失败: %w", c.baseURL, err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		msg := strings.TrimSpace(e.Error)
		if msg == "" {
			msg = strings.TrimSpace(string(data))
		}
		return fmt.Errorf("regiond %s: HTTP %d: %s", path, res.StatusCode, msg)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("解析 regiond 响应: %w", err)
		}
	}
	return nil
}
