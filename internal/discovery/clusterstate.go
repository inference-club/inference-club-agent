// Cluster state — the live half of the kubernetes discovery story (PRD 07).
// The manifest answers "what is this cluster shaped like"; ClusterState
// answers "how is it doing right now": node readiness/conditions, memory
// allocatable vs usage, GPU allocatable, and per-pod phase/restarts for the
// pods backing managed Services. Served at GET /cluster/state and proxied by
// the backend at /api/inference/providers/<id>/cluster/.
//
// Usage figures come from metrics.k8s.io (k3s ships metrics-server). Metrics
// are best-effort: when the API group is absent the snapshot still returns
// with metrics_available=false — the viz degrades to allocatable-only.
package discovery

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ClusterState is the wire format of GET /cluster/state. It is a cross-repo
// contract: the backend proxies it verbatim and the frontend's
// ClusterSnapshot types mirror it — keep the three in sync.
type ClusterState struct {
	Discovery        string      `json:"discovery"` // always "kubernetes"
	CollectedAt      time.Time   `json:"collected_at"`
	MetricsAvailable bool        `json:"metrics_available"`
	Nodes            []NodeState `json:"nodes"`
	Pods             []PodState  `json:"pods"`
}

// NodeState is the live view of one cluster node.
type NodeState struct {
	Name           string          `json:"name"`
	HostID         string          `json:"host_id"` // matches the manifest host id
	Ready          bool            `json:"ready"`
	Conditions     []NodeCondition `json:"conditions,omitempty"`
	Architecture   string          `json:"architecture,omitempty"`
	KubeletVersion string          `json:"kubelet_version,omitempty"`
	OSImage        string          `json:"os_image,omitempty"`
	Memory         MemoryState     `json:"memory"`
	GPUAllocatable int             `json:"gpu_allocatable"`
	// GPU is live VRAM/utilization from dcgm-exporter (see gpu.go). nil when
	// the node has no reachable exporter (a2/spark today) or scraping is off —
	// the viz degrades to the allocatable count above.
	GPU *NodeGPU `json:"gpu,omitempty"`
}

// NodeGPU aggregates a node's GPUs for the viz: node totals plus a per-device
// breakdown. Bytes (not MiB) so consumers don't re-scale. Mirrors the
// frontend's LiveNode.gpu (inference.club useClusterState.ts) — keep in sync.
type NodeGPU struct {
	VRAMUsedBytes  int64       `json:"vram_used_bytes"`
	VRAMTotalBytes int64       `json:"vram_total_bytes"`
	UtilPercent    int         `json:"util_percent"` // averaged across Devices
	Devices        []GPUDevice `json:"devices,omitempty"`
}

// GPUDevice is one physical GPU on a node.
type GPUDevice struct {
	Index          int    `json:"index"`
	Model          string `json:"model,omitempty"`
	VRAMUsedBytes  int64  `json:"vram_used_bytes"`
	VRAMTotalBytes int64  `json:"vram_total_bytes"`
	UtilPercent    int    `json:"util_percent"`
}

// NodeCondition mirrors the k8s node condition triple. Only non-default
// entries are interesting, but we forward them all — "never show a healthier
// cluster than kubectl does" means no filtering here.
type NodeCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// MemoryState carries bytes so no consumer re-parses k8s quantities.
// UsageBytes is 0 when metrics are unavailable (see MetricsAvailable).
type MemoryState struct {
	AllocatableBytes int64 `json:"allocatable_bytes"`
	CapacityBytes    int64 `json:"capacity_bytes"`
	UsageBytes       int64 `json:"usage_bytes"`
}

// PodState is the live view of one pod backing a managed Service.
type PodState struct {
	Name             string `json:"name"`
	Service          string `json:"service"` // manifest service name it backs
	Node             string `json:"node"`
	HostID           string `json:"host_id"`
	Phase            string `json:"phase"`
	Ready            bool   `json:"ready"`
	Restarts         int    `json:"restarts"`
	Reason           string `json:"reason,omitempty"` // e.g. ImagePullBackOff
	MemoryUsageBytes int64  `json:"memory_usage_bytes"`
}

// metricsList is the shared shape of metrics.k8s.io node and pod LISTs —
// only the fields we read.
type metricsList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Usage      map[string]string `json:"usage"` // nodes
		Containers []struct {
			Usage map[string]string `json:"usage"` // pods
		} `json:"containers"`
	} `json:"items"`
}

// ClusterState lists the cluster and assembles a live snapshot. Unlike
// Build it never validates — there is no operator input here, only facts.
func (k *Kubernetes) ClusterState(ctx context.Context) (*ClusterState, error) {
	var svcs serviceList
	if err := k.get(ctx, "/api/v1/namespaces/"+k.Namespace+"/services?labelSelector="+
		url.QueryEscape(labelManaged+"=true"), &svcs); err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	var pods podList
	if err := k.get(ctx, "/api/v1/namespaces/"+k.Namespace+"/pods", &pods); err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	var nodes nodeList
	if err := k.get(ctx, "/api/v1/nodes", &nodes); err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}

	// Best-effort metrics: a cluster without metrics-server still gets a
	// truthful (allocatable-only) snapshot.
	nodeUsage, podUsage := map[string]int64{}, map[string]int64{}
	metricsOK := true
	var nm metricsList
	if err := k.get(ctx, "/apis/metrics.k8s.io/v1beta1/nodes", &nm); err != nil {
		metricsOK = false
	} else {
		for _, it := range nm.Items {
			nodeUsage[it.Metadata.Name] = parseQuantityBytes(it.Usage["memory"])
		}
	}
	var pm metricsList
	if err := k.get(ctx, "/apis/metrics.k8s.io/v1beta1/namespaces/"+k.Namespace+"/pods", &pm); err != nil {
		metricsOK = false
	} else {
		for _, it := range pm.Items {
			var total int64
			for _, c := range it.Containers {
				total += parseQuantityBytes(c.Usage["memory"])
			}
			podUsage[it.Metadata.Name] = total
		}
	}

	// Live GPU stats (VRAM, util) from dcgm-exporter, scraped per node. Like
	// the metrics above this is best-effort: nodes without a reachable exporter
	// are simply absent from the map and report GPU=nil.
	gpuByNode := k.scrapeNodeGPUs(ctx, nodes.Items)

	return k.assembleState(svcs.Items, pods.Items, nodes.Items, nodeUsage, podUsage, gpuByNode, metricsOK), nil
}

// assembleState is pure mapping, kept separate so tests can drive it.
func (k *Kubernetes) assembleState(svcs []k8sService, pods []k8sPod, nodes []k8sNode,
	nodeUsage, podUsage map[string]int64, gpuByNode map[string]*NodeGPU, metricsOK bool) *ClusterState {

	st := &ClusterState{
		Discovery:        "kubernetes",
		CollectedAt:      time.Now().UTC(),
		MetricsAvailable: metricsOK,
		Nodes:            []NodeState{},
		Pods:             []PodState{},
	}

	hostIDByNode := map[string]string{}
	for _, n := range nodes {
		hostIDByNode[n.Metadata.Name] = hostID(n)
		ns := NodeState{
			Name:           n.Metadata.Name,
			HostID:         hostID(n),
			Architecture:   n.Status.NodeInfo.Architecture,
			KubeletVersion: n.Status.NodeInfo.KubeletVersion,
			OSImage:        n.Status.NodeInfo.OSImage,
			Memory: MemoryState{
				AllocatableBytes: parseQuantityBytes(n.Status.Allocatable["memory"]),
				CapacityBytes:    parseQuantityBytes(n.Status.Capacity["memory"]),
				UsageBytes:       nodeUsage[n.Metadata.Name],
			},
		}
		ns.GPUAllocatable, _ = strconv.Atoi(n.Status.Allocatable["nvidia.com/gpu"])
		ns.GPU = gpuByNode[n.Metadata.Name]
		for _, c := range n.Status.Conditions {
			ns.Conditions = append(ns.Conditions, NodeCondition{
				Type: c.Type, Status: c.Status, Reason: c.Reason,
			})
			if c.Type == "Ready" && c.Status == "True" {
				ns.Ready = true
			}
		}
		st.Nodes = append(st.Nodes, ns)
	}
	sort.Slice(st.Nodes, func(i, j int) bool { return st.Nodes[i].Name < st.Nodes[j].Name })

	// Every pod matching a managed Service's selector is reported — not just
	// the one Build picked as the backing pod. A crash-looping replacement
	// pod next to a Running one is exactly the degradation the viz must show.
	for _, svc := range svcs {
		if len(svc.Spec.Selector) == 0 {
			continue // external endpoint: no pods to report
		}
		for _, p := range pods {
			if !selectorMatches(svc.Spec.Selector, p.Metadata.Labels) {
				continue
			}
			ps := PodState{
				Name:             p.Metadata.Name,
				Service:          svc.Metadata.Name,
				Node:             p.Spec.NodeName,
				HostID:           hostIDByNode[p.Spec.NodeName],
				Phase:            p.Status.Phase,
				MemoryUsageBytes: podUsage[p.Metadata.Name],
			}
			ready := len(p.Status.ContainerStatuses) > 0
			for _, cs := range p.Status.ContainerStatuses {
				ps.Restarts += cs.RestartCount
				if !cs.Ready {
					ready = false
				}
				if w, ok := cs.State["waiting"]; ok && w.Reason != "" && ps.Reason == "" {
					ps.Reason = w.Reason
				}
			}
			ps.Ready = ready
			st.Pods = append(st.Pods, ps)
		}
	}
	sort.Slice(st.Pods, func(i, j int) bool {
		if st.Pods[i].Service != st.Pods[j].Service {
			return st.Pods[i].Service < st.Pods[j].Service
		}
		return st.Pods[i].Name < st.Pods[j].Name
	})
	return st
}

func selectorMatches(selector, labels map[string]string) bool {
	for key, val := range selector {
		if labels[key] != val {
			return false
		}
	}
	return true
}

// parseQuantityBytes converts a k8s resource quantity ("16323120Ki", "2Gi",
// "129M", "1073741824") to bytes. Unknown forms return 0 — a missing bar in
// the viz beats a wrong one.
func parseQuantityBytes(q string) int64 {
	q = strings.TrimSpace(q)
	if q == "" {
		return 0
	}
	mult := int64(1)
	for suffix, m := range map[string]int64{
		"Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40, "Pi": 1 << 50,
		"k": 1e3, "M": 1e6, "G": 1e9, "T": 1e12, "P": 1e15,
	} {
		if strings.HasSuffix(q, suffix) {
			mult = m
			q = strings.TrimSuffix(q, suffix)
			break
		}
	}
	n, err := strconv.ParseFloat(q, 64)
	if err != nil || n < 0 {
		return 0
	}
	return int64(n * float64(mult))
}
