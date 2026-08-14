package ui

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/service"
)

//go:embed static/*
var staticFiles embed.FS

type NodeView struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Kind      string `json:"kind"`
	Role      string `json:"role,omitempty"`
	Parent    string `json:"parent,omitempty"`
	Status    string `json:"status"`
	Readiness string `json:"readiness,omitempty"`
	Outcome   string `json:"outcome,omitempty"`
}

type Snapshot struct {
	ReadOnly    bool                      `json:"readOnly"`
	Project     map[string]string         `json:"project"`
	Status      service.OperationalStatus `json:"status"`
	Nodes       []NodeView                `json:"nodes"`
	Edges       []domain.EdgeDefinition   `json:"edges"`
	Attempts    []domain.Attempt          `json:"attempts"`
	Leases      []domain.RoleLease        `json:"leases"`
	Incidents   []domain.Incident         `json:"incidents"`
	Resources   []domain.ResourceLease    `json:"resources"`
	History     service.HistoryPage       `json:"history"`
	RefreshedAt string                    `json:"refreshedAt"`
}

func Handler(svc *service.Service) http.Handler {
	mux := http.NewServeMux()
	assets, _ := fs.Sub(staticFiles, "static")
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			return
		}
		serveAsset(w, r, assets, "index.html", "text/html; charset=utf-8")
	})
	mux.HandleFunc("/assets/app.css", func(w http.ResponseWriter, r *http.Request) {
		serveAsset(w, r, assets, "app.css", "text/css; charset=utf-8")
	})
	mux.HandleFunc("/assets/app.js", func(w http.ResponseWriter, r *http.Request) {
		serveAsset(w, r, assets, "app.js", "text/javascript; charset=utf-8")
	})
	mux.HandleFunc("/api/v1/snapshot", apiEndpoint(func(w http.ResponseWriter, r *http.Request) error {
		if err := requireQuery(r, nil); err != nil {
			return err
		}
		snapshot, err := buildSnapshot(svc)
		if err != nil {
			return err
		}
		return writeAPIResult(w, snapshot, nil)
	}))
	registerExplorerAPI(mux, svc)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w.Header())
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "read-only UI", http.StatusMethodNotAllowed)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func Serve(ctx context.Context, svc *service.Service, address string, open bool, announcements io.Writer) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("read-only UI may bind only to loopback")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: Handler(svc), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	url := "http://" + listener.Addr().String() + "/"
	if announcements != nil {
		_, _ = fmt.Fprintf(announcements, "DAGrail read-only UI: %s\n", url)
	}
	if open {
		if err := openBrowser(url); err != nil && announcements != nil {
			_, _ = fmt.Fprintf(announcements, "Browser was not opened automatically: %v\n", err)
		}
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func buildSnapshot(svc *service.Service) (Snapshot, error) {
	state, err := svc.State()
	if err != nil {
		return Snapshot{}, err
	}
	status, err := svc.Status()
	if err != nil {
		return Snapshot{}, err
	}
	after := uint64(0)
	if state.HeadSequence > 50 {
		after = state.HeadSequence - 50
	}
	history, err := svc.History(after, 50)
	if err != nil {
		return Snapshot{}, err
	}
	result := Snapshot{
		ReadOnly: true, Project: map[string]string{"id": svc.Project.Config.ProjectID, "name": svc.Project.Config.Name}, Status: status, History: history,
		Nodes: []NodeView{}, Edges: []domain.EdgeDefinition{}, Attempts: []domain.Attempt{}, Leases: []domain.RoleLease{}, Incidents: []domain.Incident{}, Resources: []domain.ResourceLease{},
		RefreshedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if state.Graph != nil {
		result.Edges = append(result.Edges, state.Graph.Spec.Edges...)
		for _, node := range state.Graph.Spec.Nodes {
			runtime := state.Nodes[node.ID]
			result.Nodes = append(result.Nodes, NodeView{ID: node.ID, Title: node.Title, Kind: node.Kind, Role: node.Role, Parent: node.Parent, Status: runtime.Status, Outcome: runtime.Outcome})
		}
	}
	for _, attempt := range state.Attempts {
		result.Attempts = append(result.Attempts, attempt)
	}
	for _, lease := range state.Leases {
		result.Leases = append(result.Leases, lease)
	}
	for _, incident := range state.Incidents {
		result.Incidents = append(result.Incidents, incident)
	}
	for _, resource := range state.Resources {
		result.Resources = append(result.Resources, resource)
	}
	sort.Slice(result.Nodes, func(i, j int) bool { return result.Nodes[i].ID < result.Nodes[j].ID })
	sort.Slice(result.Edges, func(i, j int) bool { return result.Edges[i].ID < result.Edges[j].ID })
	sort.Slice(result.Attempts, func(i, j int) bool { return result.Attempts[i].ID < result.Attempts[j].ID })
	sort.Slice(result.Leases, func(i, j int) bool { return result.Leases[i].RoleID < result.Leases[j].RoleID })
	sort.Slice(result.Incidents, func(i, j int) bool { return result.Incidents[i].ID < result.Incidents[j].ID })
	sort.Slice(result.Resources, func(i, j int) bool { return result.Resources[i].ID < result.Resources[j].ID })
	return result, nil
}

func serveAsset(w http.ResponseWriter, r *http.Request, assets fs.FS, name, contentType string) {
	data, err := fs.ReadFile(assets, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(data)
}

func setSecurityHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("X-DAGrail-Read-Only", "true")
}

func openBrowser(url string) error {
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command, args = "open", []string{url}
	case "windows":
		command, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		command, args = "xdg-open", []string{url}
	}
	if strings.TrimSpace(command) == "" {
		return fmt.Errorf("no browser launcher")
	}
	return exec.Command(command, args...).Start()
}
