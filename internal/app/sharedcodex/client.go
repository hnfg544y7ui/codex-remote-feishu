// Package sharedcodex speaks only to an explicitly enrolled local backend owner.
// A configured CLI path is never evidence of the shared server's version.
package sharedcodex

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Enrollment struct {
	SchemaVersion int    `json:"schemaVersion"`
	Endpoint      string `json:"endpoint"`
	ControlURL    string `json:"controlURL"`
	Token         string `json:"token"`
}

type Status struct {
	Generation      uint64 `json:"generation"`
	Endpoint        string `json:"endpoint"`
	Version         string `json:"version"`
	PreviousVersion string `json:"previousVersion"`
	State           string `json:"state"`
	Busy            bool   `json:"busy"`
	Problem         string `json:"problem"`
}

type Upgrade struct {
	Version         string `json:"version"`
	ExpectedVersion string `json:"expectedVersion"`
	Rollback        bool   `json:"rollback"`
}

type Maintenance interface {
	Status(context.Context) (Status, error)
	Upgrade(context.Context, Upgrade) (Status, error)
}

type Client struct {
	enrollment Enrollment
	http       *http.Client
}

func EnrollmentPath(stateDir, endpoint string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(endpoint)))
	return filepath.Join(stateDir, "shared-backend-owners", hex.EncodeToString(digest[:])+".json")
}

func loopbackURL(raw, scheme string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != scheme || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Port() == "" {
		return false
	}
	// Literal loopback only: DNS must not redirect privileged local maintenance.
	ip := net.ParseIP(u.Hostname())
	return ip != nil && ip.IsLoopback()
}

func Open(stateDir, endpoint string) (*Client, error) {
	path := EnrollmentPath(stateDir, endpoint)
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("共享后端尚未登记升级宿主：%w", err)
	}
	if info.Size() > 8192 || !info.Mode().IsRegular() || !privateEnrollment(path, info) {
		return nil, fmt.Errorf("共享后端登记文件必须是仅当前用户可读写的普通文件")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取共享后端登记失败：%w", err)
	}
	var e Enrollment
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, fmt.Errorf("共享后端登记格式无效")
	}
	if e.SchemaVersion != 1 || e.Endpoint != endpoint || !loopbackURL(endpoint, "ws") || !loopbackURL(e.ControlURL, "http") || len(e.Token) != 64 {
		return nil, fmt.Errorf("共享后端登记与当前本地后端不匹配")
	}
	if _, err := hex.DecodeString(e.Token); err != nil {
		return nil, fmt.Errorf("共享后端登记凭据无效")
	}
	return &Client{enrollment: e, http: &http.Client{
		Transport:     &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext},
		CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("共享后端控制不允许重定向") },
	}}, nil
}

func (c *Client) request(ctx context.Context, method, path string, body any) (Status, error) {
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return Status{}, err
		}
	}
	r, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.enrollment.ControlURL, "/")+path, bytes.NewReader(data))
	if err != nil {
		return Status{}, err
	}
	r.Header.Set("Authorization", "Bearer "+c.enrollment.Token)
	r.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(r)
	if err != nil {
		return Status{}, fmt.Errorf("共享后端宿主不可达或维护结果尚未确认：%w", err)
	}
	defer response.Body.Close()
	var status Status
	if err := json.NewDecoder(io.LimitReader(response.Body, 16384)).Decode(&status); err != nil {
		return Status{}, fmt.Errorf("共享后端宿主返回无效状态")
	}
	if status.Endpoint != c.enrollment.Endpoint {
		return Status{}, fmt.Errorf("共享后端宿主身份不匹配")
	}
	if response.StatusCode != http.StatusOK || status.Problem != "" {
		return status, fmt.Errorf("共享后端维护失败：%s", status.Problem)
	}
	if status.Version == "" {
		return status, fmt.Errorf("共享后端未报告实际运行版本")
	}
	return status, nil
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return c.request(ctx, http.MethodGet, "/v1/status", nil)
}

func (c *Client) Upgrade(ctx context.Context, upgrade Upgrade) (Status, error) {
	return c.request(ctx, http.MethodPost, "/v1/upgrade", upgrade)
}
