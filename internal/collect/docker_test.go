package collect

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/akaike-byob/dokploy-sentinel/internal/health"
)

// fakeDocker starts an httptest-style Docker daemon on a unix socket and returns
// a client pointed at it.
func fakeDocker(t *testing.T) *DockerClient {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "docker.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/_ping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", "1.44")
		w.WriteHeader(200)
		w.Write([]byte("OK"))
	})
	mux.HandleFunc("/v1.44/containers/json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
		  {"Id":"c1","Names":["/web"],"Image":"web:latest","State":"running","Status":"Up","Labels":{"com.docker.swarm.service.name":"web"}},
		  {"Id":"c2","Names":["/db"],"Image":"pg:15","State":"exited","Status":"Exited (137)","Labels":{"com.docker.compose.project":"proj","com.docker.compose.service":"db"}}
		]`))
	})
	mux.HandleFunc("/v1.44/containers/c1/json", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"Id":"c1","Name":"/web","State":{"Status":"running","Running":true,"Pid":0,"ExitCode":0,"OOMKilled":false,"Health":{"Status":"healthy"}},
		  "RestartCount":0,"Config":{"Image":"web:latest","Labels":{"com.docker.swarm.service.name":"web"},"Healthcheck":{"Test":["CMD","true"]}},
		  "HostConfig":{"Memory":0,"Privileged":false,"RestartPolicy":{"Name":"always"}}}`))
	})
	mux.HandleFunc("/v1.44/containers/c2/json", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"Id":"c2","Name":"/db","State":{"Status":"exited","Running":false,"Pid":0,"ExitCode":137,"OOMKilled":true,"FinishedAt":"2026-07-14T09:00:00Z"},
		  "RestartCount":4,"Config":{"Image":"pg:15","Labels":{"com.docker.compose.project":"proj","com.docker.compose.service":"db"}},
		  "HostConfig":{"Memory":536870912,"Privileged":true,"RestartPolicy":{"Name":"no"}}}`))
	})

	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return NewDockerClient(sock, 5*time.Second)
}

func TestDockerCollectAndInspect(t *testing.T) {
	dc := fakeDocker(t)
	di := collectDocker(context.Background(), Options{
		Docker: dc, InspectConcurrency: 4, Deadline: 5 * time.Second,
		ProcRoot: t.TempDir(), CgroupRoot: t.TempDir(),
	})
	if di.Health != health.OK || !di.Reachable {
		t.Fatalf("docker health = %s (%s)", di.Health, di.Err)
	}
	if di.APIVersion != "1.44" {
		t.Errorf("negotiated version = %q, want 1.44", di.APIVersion)
	}
	if len(di.Containers) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(di.Containers))
	}

	byID := map[string]Container{}
	for _, c := range di.Containers {
		byID[c.ID] = c
	}
	web := byID["c1"]
	if web.MemoryLimit != 0 {
		t.Errorf("web should be unbounded (Memory 0), got %d", web.MemoryLimit)
	}
	if web.ServiceKey() != "web" || !web.HasHealthcheck || web.RestartPolicy != "always" {
		t.Errorf("web inspect mapping wrong: %+v", web)
	}
	db := byID["c2"]
	if db.ExitCode != 137 || !db.OOMKilled {
		t.Errorf("db should be exit-137 OOMKilled, got exit=%d oom=%v", db.ExitCode, db.OOMKilled)
	}
	if db.ServiceKey() != "proj_db" {
		t.Errorf("compose service key = %q, want proj_db", db.ServiceKey())
	}
	if db.MemoryLimit != 536870912 || !db.Privileged {
		t.Errorf("db host config mapping wrong: %+v", db)
	}
	if db.FinishedAt.IsZero() {
		t.Errorf("db FinishedAt should parse")
	}
}

func TestNegotiateVersion(t *testing.T) {
	cases := map[string]string{"1.41": "1.41", "1.44": "1.44", "1.99": "1.44", "": "1.44", "garbage": "1.44"}
	for in, want := range cases {
		if got := negotiateVersion(in); got != want {
			t.Errorf("negotiateVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDockerNilIsUnknown(t *testing.T) {
	di := collectDocker(context.Background(), Options{Docker: nil})
	if di.Health != health.UNKNOWN {
		t.Fatalf("nil docker client should be UNKNOWN, got %s", di.Health)
	}
}
