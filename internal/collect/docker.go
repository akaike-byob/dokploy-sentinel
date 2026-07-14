package collect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ourMaxAPIVersion is the newest Docker API version this client will negotiate
// down to. Baseline is 1.41 (rejected by nothing modern).
const ourMaxAPIVersion = "1.44"

// DockerClient talks raw HTTP over a unix socket (no SDK). Every call takes a
// context so the caller's per-call + per-run deadlines bound wall time.
type DockerClient struct {
	hc         *http.Client
	socket     string
	apiVersion string // negotiated; "" until Ping succeeds
}

// NewDockerClient builds a client that dials the given unix socket. perCall
// bounds a single request; the caller additionally bounds the whole run.
func NewDockerClient(socket string, perCall time.Duration) *DockerClient {
	dialer := &net.Dialer{Timeout: perCall}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socket)
		},
		MaxIdleConns:          8,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: perCall,
	}
	return &DockerClient{
		hc:     &http.Client{Transport: transport, Timeout: perCall},
		socket: socket,
	}
}

// APIVersion returns the negotiated API version (empty before a successful Ping).
func (d *DockerClient) APIVersion() string { return d.apiVersion }

// Ping negotiates the API version via GET /_ping. It returns a hint string when
// the failure is a permission problem, so the operator can be told to run as
// root / join the docker group.
func (d *DockerClient) Ping(ctx context.Context) (hint string, err error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/_ping", nil)
	resp, err := d.hc.Do(req)
	if err != nil {
		return permissionHint(err), fmt.Errorf("docker ping: %w", err)
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("docker ping: status %d", resp.StatusCode)
	}
	d.apiVersion = negotiateVersion(resp.Header.Get("Api-Version"))
	return "", nil
}

// ListContainers returns all containers (including exited, to catch crash-loop /
// OOM corpses).
func (d *DockerClient) ListContainers(ctx context.Context) ([]dockerListItem, error) {
	body, err := d.get(ctx, "/containers/json?all=true")
	if err != nil {
		return nil, err
	}
	var items []dockerListItem
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("decode container list: %w", err)
	}
	return items, nil
}

// Inspect returns the full inspect payload for one container.
func (d *DockerClient) Inspect(ctx context.Context, id string) (*dockerInspect, error) {
	body, err := d.get(ctx, "/containers/"+id+"/json")
	if err != nil {
		return nil, err
	}
	var ins dockerInspect
	if err := json.Unmarshal(body, &ins); err != nil {
		return nil, fmt.Errorf("decode inspect %s: %w", short(id), err)
	}
	return &ins, nil
}

// get performs a version-prefixed GET and returns the body on 2xx.
func (d *DockerClient) get(ctx context.Context, path string) ([]byte, error) {
	url := "http://docker" + d.versionPrefix() + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer drain(resp)
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("docker GET %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func (d *DockerClient) versionPrefix() string {
	if d.apiVersion == "" {
		return ""
	}
	return "/v" + d.apiVersion
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()
}

// permissionHint recognizes EACCES on the socket.
func permissionHint(err error) string {
	if errors.Is(err, syscall.EACCES) || strings.Contains(err.Error(), "permission denied") {
		return "run as root or add the service user to the docker group"
	}
	return ""
}

// negotiateVersion returns min(ourMax, host). Empty/garbage host → ourMax.
func negotiateVersion(host string) string {
	host = strings.TrimSpace(host)
	if host == "" || !looksLikeVersion(host) {
		return ourMaxAPIVersion
	}
	if compareAPIVersions(host, ourMaxAPIVersion) < 0 {
		return host
	}
	return ourMaxAPIVersion
}

func looksLikeVersion(s string) bool {
	for _, r := range s {
		if r != '.' && (r < '0' || r > '9') {
			return false
		}
	}
	return strings.Contains(s, ".")
}

// compareAPIVersions compares dotted numeric versions ("1.44" vs "1.41").
func compareAPIVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var ai, bi int
		if i < len(as) {
			ai, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			bi, _ = strconv.Atoi(bs[i])
		}
		if ai != bi {
			if ai < bi {
				return -1
			}
			return 1
		}
	}
	return 0
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// parseDockerTime parses Docker's RFC3339Nano timestamps; the zero value
// "0001-01-01T00:00:00Z" and parse failures return the zero time.
func parseDockerTime(s string) time.Time {
	if s == "" || strings.HasPrefix(s, "0001-01-01") {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// ---- raw JSON shapes (only the fields we consume) ----

type dockerListItem struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Status string            `json:"Status"`
	Labels map[string]string `json:"Labels"`
}

type dockerInspect struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	State struct {
		Status     string `json:"Status"`
		Running    bool   `json:"Running"`
		Pid        int    `json:"Pid"`
		ExitCode   int    `json:"ExitCode"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
		OOMKilled  bool   `json:"OOMKilled"`
		Health     *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	RestartCount int `json:"RestartCount"`
	Config       struct {
		Image       string            `json:"Image"`
		Labels      map[string]string `json:"Labels"`
		Healthcheck *struct {
			Test []string `json:"Test"`
		} `json:"Healthcheck"`
	} `json:"Config"`
	HostConfig struct {
		Memory        int64 `json:"Memory"`
		Privileged    bool  `json:"Privileged"`
		RestartPolicy struct {
			Name string `json:"Name"`
		} `json:"RestartPolicy"`
	} `json:"HostConfig"`
}

// hasHealthcheck reports whether a real healthcheck is configured (not disabled
// with ["NONE"]).
func (ins *dockerInspect) hasHealthcheck() bool {
	hc := ins.Config.Healthcheck
	if hc == nil || len(hc.Test) == 0 {
		return false
	}
	return !(len(hc.Test) == 1 && hc.Test[0] == "NONE")
}
