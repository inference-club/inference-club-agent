// Package discovery builds the agent's manifest from somewhere other than
// agent.yaml. The kubernetes mode treats the cluster as the source of truth:
// Services labeled inference-club.com/managed=true ARE the service list, the
// Pods behind them say which node (and with what exact image/command) each
// service runs, and Nodes carry the GPU facts agent.yaml used to hand-type.
//
// Deliberately implemented against the raw Kubernetes REST API with stdlib
// only — no client-go. We need four namespace-scoped LISTs on a poll loop,
// not informers, and client-go would dwarf every other dependency in a binary
// that ships to operators (the repo intentionally avoids a committed go.sum).
//
// Label/annotation schema (see home-cluster/docs/02-k8s-discovery.md):
//
//	labels:
//	  inference-club.com/managed: "true"      required — the discovery selector
//	  inference-club.com/type:    tts         llm|stt|tts|image|mesh|music|video (default llm)
//	  inference-club.com/engine:  other       manifest engine enum (default other)
//	annotations:
//	  inference-club.com/models:          YAML list of manifest.Model
//	  inference-club.com/features:        comma list (e.g. "timestamps")
//	  inference-club.com/base-path:       appended to the service URL (e.g. "/v1")
//	  inference-club.com/port:            port name or number when the Service has several
//	  inference-club.com/api-key-secret:  Secret name; its "api-key" value is sent
//	                                      upstream as a Bearer (never uploaded)
package discovery

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/briancaffey/inference-club-agent/host-agent/internal/manifest"
)

const (
	labelManaged = "inference-club.com/managed"
	labelType    = "inference-club.com/type"
	labelEngine  = "inference-club.com/engine"
	labelBox     = "inference-club.com/box" // node label → manifest host id

	annModels       = "inference-club.com/models"
	annFeatures     = "inference-club.com/features"
	annBasePath     = "inference-club.com/base-path"
	annPort         = "inference-club.com/port"
	annAPIKeySecret = "inference-club.com/api-key-secret"

	saDir = "/var/run/secrets/kubernetes.io/serviceaccount"
)

// Kubernetes lists cluster state and assembles a manifest.Manifest.
type Kubernetes struct {
	// AgentName becomes manifest.Agent.Name — the Provider lookup key.
	AgentName string
	// Namespace scopes Service/Pod/EndpointSlice/Secret reads.
	Namespace string

	// APIBase, TokenPath and client are settable for tests; zero values mean
	// in-cluster defaults (KUBERNETES_SERVICE_HOST + mounted serviceaccount).
	APIBase   string
	TokenPath string
	Client    *http.Client

	// DCGMPort is the hostPort dcgm-exporter listens on for live GPU stats
	// (VRAM, util) scraped per node in ClusterState. 0 disables GPU scraping —
	// the zero value, so tests never touch the network. NewInCluster defaults
	// it to 9400 (see clusters/home/monitoring/dcgm-exporter.yaml).
	DCGMPort int
}

// NewInCluster configures discovery from the standard in-cluster environment.
func NewInCluster(agentName, namespace string) (*Kubernetes, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, fmt.Errorf("not in a cluster: KUBERNETES_SERVICE_HOST/PORT unset")
	}
	caPEM, err := os.ReadFile(saDir + "/ca.crt")
	if err != nil {
		return nil, fmt.Errorf("read serviceaccount ca: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("serviceaccount ca.crt: no certificates parsed")
	}
	return &Kubernetes{
		AgentName: agentName,
		Namespace: namespace,
		APIBase:   "https://" + net.JoinHostPort(host, port),
		TokenPath: saDir + "/token",
		Client: &http.Client{
			Timeout:   15 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
		},
		DCGMPort: dcgmPortFromEnv(),
	}, nil
}

// dcgmPortFromEnv reads DCGM_SCRAPE_PORT (default 9400). Set it to "0" to
// disable GPU scraping entirely where dcgm-exporter isn't deployed.
func dcgmPortFromEnv() int {
	if v := os.Getenv("DCGM_SCRAPE_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			return p
		}
	}
	return 9400
}

// Build lists the cluster and assembles a validated manifest. The returned
// LoadResult mirrors manifest.Load's: Raw carries normalized YAML of what we
// built (the server stores it for display).
func (k *Kubernetes) Build(ctx context.Context) (*manifest.LoadResult, []string, error) {
	var svcs serviceList
	if err := k.get(ctx, "/api/v1/namespaces/"+k.Namespace+"/services?labelSelector="+
		url.QueryEscape(labelManaged+"=true"), &svcs); err != nil {
		return nil, nil, fmt.Errorf("list services: %w", err)
	}
	var pods podList
	if err := k.get(ctx, "/api/v1/namespaces/"+k.Namespace+"/pods", &pods); err != nil {
		return nil, nil, fmt.Errorf("list pods: %w", err)
	}
	var slices endpointSliceList
	if err := k.get(ctx, "/apis/discovery.k8s.io/v1/namespaces/"+k.Namespace+"/endpointslices", &slices); err != nil {
		return nil, nil, fmt.Errorf("list endpointslices: %w", err)
	}
	var nodes nodeList
	if err := k.get(ctx, "/api/v1/nodes", &nodes); err != nil {
		return nil, nil, fmt.Errorf("list nodes: %w", err)
	}

	m := k.assemble(ctx, svcs.Items, pods.Items, slices.Items, nodes.Items)
	if errs := manifest.Validate(m); errs != nil {
		return nil, errs, nil
	}
	raw, err := yaml.Marshal(m.Redacted())
	if err != nil {
		return nil, nil, fmt.Errorf("marshal built manifest: %w", err)
	}
	return &manifest.LoadResult{Manifest: m, Raw: raw}, nil, nil
}

// assemble is pure mapping (no API calls except api-key Secret reads), kept
// separate so tests can drive it with fixture objects.
func (k *Kubernetes) assemble(ctx context.Context, svcs []k8sService, pods []k8sPod, slices []k8sEndpointSlice, nodes []k8sNode) *manifest.Manifest {
	nodeByName := map[string]k8sNode{}
	for _, n := range nodes {
		nodeByName[n.Metadata.Name] = n
	}

	// hosts accrue services as we walk Services sorted by name, so repeated
	// Builds of identical cluster state marshal to identical bytes (the poll
	// loop diffs bytes to decide whether to re-push).
	sort.Slice(svcs, func(i, j int) bool { return svcs[i].Metadata.Name < svcs[j].Metadata.Name })
	hostsByID := map[string]*manifest.Host{}
	var hostOrder []string

	hostFor := func(id string, build func() manifest.Host) *manifest.Host {
		if h, ok := hostsByID[id]; ok {
			return h
		}
		h := build()
		hostsByID[id] = &h
		hostOrder = append(hostOrder, id)
		return hostsByID[id]
	}

	for _, svc := range svcs {
		s := manifest.Service{
			Name:   svc.Metadata.Name,
			Type:   svc.Metadata.Labels[labelType],
			Engine: defaultStr(svc.Metadata.Labels[labelEngine], "other"),
			URL:    serviceURL(svc, k.Namespace),
		}
		if f := strings.TrimSpace(svc.Metadata.Annotations[annFeatures]); f != "" {
			for _, part := range strings.Split(f, ",") {
				if p := strings.TrimSpace(part); p != "" {
					s.Features = append(s.Features, p)
				}
			}
		}
		if raw := svc.Metadata.Annotations[annModels]; raw != "" {
			var models []manifest.Model
			if err := yaml.Unmarshal([]byte(raw), &models); err == nil {
				s.Models = models
			}
			// A malformed models annotation degrades to "no declared models"
			// rather than dropping the service; doctor will flag it.
		}
		if name := strings.TrimSpace(svc.Metadata.Annotations[annAPIKeySecret]); name != "" {
			if key, err := k.secretAPIKey(ctx, name); err == nil {
				s.APIKey = key
			}
		}

		if len(svc.Spec.Selector) > 0 {
			pod, ok := backingPod(svc, pods)
			if !ok {
				// Declared but not scheduled/running — drop it from the
				// manifest (the manifest reports what IS serving) and let the
				// next poll pick it up when a pod lands.
				continue
			}
			s.Command = podCommand(pod)
			node, _ := nodeByName[pod.Spec.NodeName]
			h := hostFor(hostID(node), func() manifest.Host { return hostFromNode(node) })
			h.Services = append(h.Services, s)
			continue
		}

		// Selector-less Service (e.g. LM Studio on a box's host OS): the
		// manual EndpointSlice carries the address; there is no pod, so the
		// "host" is the external endpoint itself, GPU facts unknown.
		addr := externalAddress(svc.Metadata.Name, slices)
		if addr == "" {
			continue // no endpoint → not serving → not in the manifest
		}
		h := hostFor("external-"+svc.Metadata.Name, func() manifest.Host {
			return manifest.Host{ID: "external-" + svc.Metadata.Name, Address: addr,
				Notes: "external endpoint (outside the cluster)"}
		})
		h.Services = append(h.Services, s)
	}

	hosts := make([]manifest.Host, 0, len(hostOrder))
	sort.Strings(hostOrder)
	for _, id := range hostOrder {
		hosts = append(hosts, *hostsByID[id])
	}
	return &manifest.Manifest{
		SchemaVersion: manifest.SchemaVersion,
		Discovery:     "kubernetes",
		Agent:         manifest.Agent{Name: k.AgentName},
		Hosts:         hosts,
	}
}

// --- mapping helpers --------------------------------------------------------

// serviceURL is the in-cluster DNS URL: kube-proxy routes it for selector'd
// AND selector-less Services alike, so the router needs no special cases.
func serviceURL(svc k8sService, namespace string) string {
	port := 0
	want := strings.TrimSpace(svc.Metadata.Annotations[annPort])
	for i, p := range svc.Spec.Ports {
		if i == 0 && want == "" {
			port = p.Port
			break
		}
		if want != "" && (p.Name == want || strconv.Itoa(p.Port) == want) {
			port = p.Port
			break
		}
	}
	base := strings.TrimSpace(svc.Metadata.Annotations[annBasePath])
	if base != "" && !strings.HasPrefix(base, "/") {
		base = "/" + base
	}
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d%s",
		svc.Metadata.Name, namespace, port, strings.TrimSuffix(base, "/"))
}

// backingPod picks the pod that represents the service: a Running pod whose
// labels satisfy the Service selector (map-equality, the only selector form
// core Services support).
func backingPod(svc k8sService, pods []k8sPod) (k8sPod, bool) {
	match := func(p k8sPod) bool {
		for key, val := range svc.Spec.Selector {
			if p.Metadata.Labels[key] != val {
				return false
			}
		}
		return true
	}
	var fallback *k8sPod
	for i, p := range pods {
		if !match(p) {
			continue
		}
		if p.Status.Phase == "Running" {
			return p, true
		}
		if fallback == nil {
			fallback = &pods[i]
		}
	}
	if fallback != nil {
		return *fallback, true
	}
	return k8sPod{}, false
}

// podCommand renders the exact runtime invocation — image, command, args —
// the thing agent.yaml's hand-typed `command:` could only approximate.
func podCommand(p k8sPod) string {
	if len(p.Spec.Containers) == 0 {
		return ""
	}
	c := p.Spec.Containers[0]
	parts := append([]string{c.Image}, c.Command...)
	parts = append(parts, c.Args...)
	cmd := strings.Join(parts, " ")
	if len(cmd) > manifest.MaxStringLen {
		cmd = cmd[:manifest.MaxStringLen]
	}
	return cmd
}

func hostID(n k8sNode) string {
	if box := n.Metadata.Labels[labelBox]; box != "" {
		return box
	}
	return defaultStr(n.Metadata.Name, "unknown-node")
}

// hostFromNode maps Node facts to a manifest Host. GPU count comes from the
// device plugin's allocatable; model/VRAM light up when GPU-feature-discovery
// is installed (its labels are absent otherwise — fields just stay empty).
func hostFromNode(n k8sNode) manifest.Host {
	h := manifest.Host{ID: hostID(n), Hostname: n.Metadata.Name}
	for _, a := range n.Status.Addresses {
		if a.Type == "InternalIP" {
			h.Address = a.Address
			break
		}
	}
	if count, _ := strconv.Atoi(n.Status.Allocatable["nvidia.com/gpu"]); count > 0 {
		gpu := &manifest.GPU{Vendor: "nvidia", Count: count,
			Model: n.Metadata.Labels["nvidia.com/gpu.product"]}
		if mib, _ := strconv.Atoi(n.Metadata.Labels["nvidia.com/gpu.memory"]); mib > 0 {
			gpu.VRAMGB = float64(mib) / 1024
		}
		h.GPU = gpu
	}
	return h
}

func externalAddress(svcName string, slices []k8sEndpointSlice) string {
	for _, sl := range slices {
		if sl.Metadata.Labels["kubernetes.io/service-name"] != svcName {
			continue
		}
		for _, ep := range sl.Endpoints {
			if len(ep.Addresses) > 0 {
				return ep.Addresses[0]
			}
		}
	}
	return ""
}

func defaultStr(v, fallback string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

// --- API plumbing -----------------------------------------------------------

// get performs an authenticated GET and decodes JSON. The token is re-read
// every call: bound serviceaccount tokens rotate, and a poll-loop client must
// pick up the refreshed file.
func (k *Kubernetes) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.APIBase+path, nil)
	if err != nil {
		return err
	}
	if k.TokenPath != "" {
		tok, err := os.ReadFile(k.TokenPath)
		if err != nil {
			return fmt.Errorf("read serviceaccount token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tok)))
	}
	resp, err := k.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (k *Kubernetes) secretAPIKey(ctx context.Context, name string) (string, error) {
	var s k8sSecret
	if err := k.get(ctx, "/api/v1/namespaces/"+k.Namespace+"/secrets/"+name, &s); err != nil {
		return "", err
	}
	b64, ok := s.Data["api-key"]
	if !ok {
		return "", fmt.Errorf("secret %s has no api-key key", name)
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("secret %s api-key: %w", name, err)
	}
	return strings.TrimSpace(string(raw)), nil
}

func (k *Kubernetes) client() *http.Client {
	if k.Client != nil {
		return k.Client
	}
	return http.DefaultClient
}

// --- minimal typed views of the k8s objects we read --------------------------

type objectMeta struct {
	Name        string            `json:"name"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
}

type serviceList struct {
	Items []k8sService `json:"items"`
}
type k8sService struct {
	Metadata objectMeta `json:"metadata"`
	Spec     struct {
		Selector map[string]string `json:"selector"`
		Ports    []struct {
			Name string `json:"name"`
			Port int    `json:"port"`
		} `json:"ports"`
	} `json:"spec"`
}

type podList struct {
	Items []k8sPod `json:"items"`
}
type k8sPod struct {
	Metadata objectMeta `json:"metadata"`
	Spec     struct {
		NodeName   string `json:"nodeName"`
		Containers []struct {
			Image   string   `json:"image"`
			Command []string `json:"command"`
			Args    []string `json:"args"`
		} `json:"containers"`
	} `json:"spec"`
	Status struct {
		Phase             string `json:"phase"`
		ContainerStatuses []struct {
			Name         string `json:"name"`
			RestartCount int    `json:"restartCount"`
			Ready        bool   `json:"ready"`
			State        map[string]struct {
				Reason string `json:"reason"`
			} `json:"state"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

type endpointSliceList struct {
	Items []k8sEndpointSlice `json:"items"`
}
type k8sEndpointSlice struct {
	Metadata  objectMeta `json:"metadata"`
	Endpoints []struct {
		Addresses []string `json:"addresses"`
	} `json:"endpoints"`
}

type nodeList struct {
	Items []k8sNode `json:"items"`
}
type k8sNode struct {
	Metadata objectMeta `json:"metadata"`
	Status   struct {
		Allocatable map[string]string `json:"allocatable"`
		Capacity    map[string]string `json:"capacity"`
		Addresses   []struct {
			Type    string `json:"type"`
			Address string `json:"address"`
		} `json:"addresses"`
		Conditions []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
			Reason string `json:"reason"`
		} `json:"conditions"`
		NodeInfo struct {
			Architecture   string `json:"architecture"`
			KubeletVersion string `json:"kubeletVersion"`
			OSImage        string `json:"osImage"`
		} `json:"nodeInfo"`
	} `json:"status"`
}

type k8sSecret struct {
	Data map[string]string `json:"data"`
}
