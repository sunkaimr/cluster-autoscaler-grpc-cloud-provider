package metrics

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sunkaimr/cluster-autoscaler-grpc-cloud-provider/nodegroup"
	"github.com/sunkaimr/cluster-autoscaler-grpc-cloud-provider/nodegroup/instance"
	"k8s.io/klog/v2"
)

const (
	namespace = "ca_grpc_cloud_provider"
)

var (
	// NodegroupMinSize is a prometheus gauge that nodegroup min nodes size.
	NodegroupMinSize = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "nodegroup_min_size",
			Help:      "nodegroup min size",
		},
		[]string{"nodegroup"},
	)

	// NodegroupMaxSize is a prometheus gauge that nodegroup max nodes size.
	NodegroupMaxSize = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "nodegroup_max_size",
			Help:      "nodegroup max size",
		},
		[]string{"nodegroup"},
	)

	// NodegroupTargetSize is a prometheus gauge that nodegroup target nodes size.
	NodegroupTargetSize = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "nodegroup_target_size",
			Help:      "nodegroup target size",
		},
		[]string{"nodegroup"},
	)

	// NodegroupNodeTotal is a prometheus gauge that nodegroup total nodes size(exclude Deleted node).
	NodegroupNodeTotal = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "nodegroup_node_total",
			Help:      "nodegroup node total(exclude Deleted node)",
		},
		[]string{"nodegroup"},
	)

	// NodegroupNodeStageStatusCount is a Prometheus gauge vector that tracks the number of nodes
	// in a nodegroup across different stages and statuses.
	NodegroupNodeStageStatusCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "nodegroup_node_stage_status_count",
			Help:      "the number of nodes in a nodegroup across different stages and status",
		},
		[]string{"nodegroup", "stage", "status"},
	)

	// MatchedMultipleNodegroup is a Prometheus gauge vector that node matched multiple nodegroup
	MatchedMultipleNodegroup = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "matched_multiple_nodegroup",
			Help:      "node matched multiple nodegroup",
		},
		[]string{"node", "nodegroup"},
	)
)

func Server(addr, path string) {
	customRegistry := prometheus.NewRegistry()

	customRegistry.MustRegister(NodegroupMinSize)
	customRegistry.MustRegister(NodegroupMaxSize)
	customRegistry.MustRegister(NodegroupTargetSize)
	customRegistry.MustRegister(NodegroupNodeTotal)
	customRegistry.MustRegister(NodegroupNodeStageStatusCount)
	customRegistry.MustRegister(MatchedMultipleNodegroup)

	http.Handle(path, promhttp.HandlerFor(
		customRegistry,
		promhttp.HandlerOpts{
			Timeout: time.Second * 3,
		},
	))

	go func() {
		klog.V(1).Infof("metrics server listen at: %s%s", addr, path)
		if err := http.ListenAndServe(addr, nil); err != nil {
			klog.Fatalf("failed to run metrics server, %s", err)
		}
	}()

	refresh()
}

func refresh() {
	tick := time.Tick(time.Second * 5)
	for {
		start := time.Now()
		klog.V(6).Infof("refresh metrics...")

		NodegroupMinSize.Reset()
		NodegroupMaxSize.Reset()
		NodegroupTargetSize.Reset()
		NodegroupNodeTotal.Reset()
		NodegroupNodeStageStatusCount.Reset()

		for _, ng := range nodegroup.GetNodeGroups().List() {
			NodegroupMinSize.WithLabelValues(ng.Id).Set(float64(ng.MinSize))
			NodegroupMaxSize.WithLabelValues(ng.Id).Set(float64(ng.MaxSize))
			NodegroupTargetSize.WithLabelValues(ng.Id).Set(float64(ng.TargetSize))

			totalNode := 0
			groupNode := groupNodeByStageAndStatus(ng.Instances)
			for key, val := range groupNode {
				ss := strings.Split(key, ":")
				if len(ss) != 2 {
					continue
				}
				stage, status := ss[0], ss[1]
				NodegroupNodeStageStatusCount.WithLabelValues(ng.Id, stage, status).Set(float64(val))
				if stage != string(instance.StageDeleted) {
					totalNode += val
				}
			}
			NodegroupNodeTotal.WithLabelValues(ng.Id).Set(float64(totalNode))
		}

		MatchedMultipleNodegroup.Reset()
		for node, ngs := range nodegroup.GetNodeGroups().GetMatchedMultipleNodeGroup() {
			MatchedMultipleNodegroup.WithLabelValues(node, strings.Join(ngs, ",")).Set(float64(len(ngs)))
		}

		klog.V(6).Infof("finish refresh metrics, cost: %v", time.Now().Sub(start))
		<-tick
	}
}

func groupNodeByStageAndStatus(instances instance.InstanceList) map[string]int {
	stageStatusMap := map[string]int{
		// 只暴露非零的指标，若需全部暴露需要放开注释
		fmt.Sprintf("%s:%s", instance.StagePending, instance.StatusInit):      0,
		fmt.Sprintf("%s:%s", instance.StagePending, instance.StatusInProcess): 0,
		fmt.Sprintf("%s:%s", instance.StagePending, instance.StatusSuccess):   0,
		fmt.Sprintf("%s:%s", instance.StagePending, instance.StatusFailed):    0,
		fmt.Sprintf("%s:%s", instance.StagePending, instance.StatusUnknown):   0,

		fmt.Sprintf("%s:%s", instance.StageCreating, instance.StatusInit):      0,
		fmt.Sprintf("%s:%s", instance.StageCreating, instance.StatusInProcess): 0,
		fmt.Sprintf("%s:%s", instance.StageCreating, instance.StatusSuccess):   0,
		fmt.Sprintf("%s:%s", instance.StageCreating, instance.StatusFailed):    0,
		fmt.Sprintf("%s:%s", instance.StageCreating, instance.StatusUnknown):   0,

		fmt.Sprintf("%s:%s", instance.StageCreated, instance.StatusInit):      0,
		fmt.Sprintf("%s:%s", instance.StageCreated, instance.StatusInProcess): 0,
		fmt.Sprintf("%s:%s", instance.StageCreated, instance.StatusSuccess):   0,
		fmt.Sprintf("%s:%s", instance.StageCreated, instance.StatusFailed):    0,
		fmt.Sprintf("%s:%s", instance.StageCreated, instance.StatusUnknown):   0,

		fmt.Sprintf("%s:%s", instance.StageJoined, instance.StatusInit):      0,
		fmt.Sprintf("%s:%s", instance.StageJoined, instance.StatusInProcess): 0,
		fmt.Sprintf("%s:%s", instance.StageJoined, instance.StatusSuccess):   0,
		fmt.Sprintf("%s:%s", instance.StageJoined, instance.StatusFailed):    0,
		fmt.Sprintf("%s:%s", instance.StageJoined, instance.StatusUnknown):   0,

		fmt.Sprintf("%s:%s", instance.StageRunning, instance.StatusInit):      0,
		fmt.Sprintf("%s:%s", instance.StageRunning, instance.StatusInProcess): 0,
		fmt.Sprintf("%s:%s", instance.StageRunning, instance.StatusSuccess):   0,
		fmt.Sprintf("%s:%s", instance.StageRunning, instance.StatusFailed):    0,
		fmt.Sprintf("%s:%s", instance.StageRunning, instance.StatusUnknown):   0,

		fmt.Sprintf("%s:%s", instance.StagePendingDeletion, instance.StatusInit):      0,
		fmt.Sprintf("%s:%s", instance.StagePendingDeletion, instance.StatusInProcess): 0,
		fmt.Sprintf("%s:%s", instance.StagePendingDeletion, instance.StatusSuccess):   0,
		fmt.Sprintf("%s:%s", instance.StagePendingDeletion, instance.StatusFailed):    0,
		fmt.Sprintf("%s:%s", instance.StagePendingDeletion, instance.StatusUnknown):   0,

		fmt.Sprintf("%s:%s", instance.StageDeleting, instance.StatusInit):      0,
		fmt.Sprintf("%s:%s", instance.StageDeleting, instance.StatusInProcess): 0,
		fmt.Sprintf("%s:%s", instance.StageDeleting, instance.StatusSuccess):   0,
		fmt.Sprintf("%s:%s", instance.StageDeleting, instance.StatusFailed):    0,
		fmt.Sprintf("%s:%s", instance.StageDeleting, instance.StatusUnknown):   0,

		fmt.Sprintf("%s:%s", instance.StageDeleted, instance.StatusInit):      0,
		fmt.Sprintf("%s:%s", instance.StageDeleted, instance.StatusInProcess): 0,
		fmt.Sprintf("%s:%s", instance.StageDeleted, instance.StatusSuccess):   0,
		fmt.Sprintf("%s:%s", instance.StageDeleted, instance.StatusFailed):    0,
		fmt.Sprintf("%s:%s", instance.StageDeleted, instance.StatusUnknown):   0,
	}

	for _, ins := range instances {
		key := fmt.Sprintf("%s:%s", ins.Stage, ins.Status)
		stageStatusMap[key] = stageStatusMap[key] + 1
	}
	return stageStatusMap
}
