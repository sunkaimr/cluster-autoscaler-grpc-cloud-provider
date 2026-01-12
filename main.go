package main

import (
	"context"
	"flag"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/pflag"
	"github.com/sunkaimr/cluster-autoscaler-grpc-cloud-provider/pkg/utils"
	"github.com/sunkaimr/cluster-autoscaler-grpc-cloud-provider/server"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/component-base/config/options"

	"github.com/sunkaimr/cluster-autoscaler-grpc-cloud-provider/nodegroup"
	"github.com/sunkaimr/cluster-autoscaler-grpc-cloud-provider/nodegroup/metrics"
	"github.com/sunkaimr/cluster-autoscaler-grpc-cloud-provider/pkg/common"
	"github.com/sunkaimr/cluster-autoscaler-grpc-cloud-provider/wrapper"
	"google.golang.org/grpc"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	cloudBuilder "k8s.io/autoscaler/cluster-autoscaler/cloudprovider/builder"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider/externalgrpc/protos"
	"k8s.io/autoscaler/cluster-autoscaler/config"
	kubeFlag "k8s.io/component-base/cli/flag"
	componentbaseconfig "k8s.io/component-base/config"
	"k8s.io/klog/v2"
)

const (
	defaultLeaseDuration = 15 * time.Second
	defaultRenewDeadline = 10 * time.Second
	defaultRetryPeriod   = 2 * time.Second
)

var (
	gitCommit string
	buildTime string
	goVersion string
	version   string

	// flags needed by the external grpc provider service
	httpAddress       = flag.String("http-address", ":8080", "The address to listen on for http server")
	grpcAddress       = flag.String("grpc-address", ":8086", "The address to expose the grpc service.")
	metricsAddress    = flag.String("metrics-address", ":8087", "The address to listen on for Prometheus scrapes.")
	metricsPath       = flag.String("metrics-path", "/metrics", "The path to publish Prometheus metrics to.")
	cloudProviderFlag = flag.String("cloud-provider", cloudprovider.ExternalGrpcProviderName, "cloud provider type, only support 'externalgrpc'")
	cloudConfig       = flag.String("cloud-config", "cloud-config.cfg", "The path to the cloud provider configuration file.")
	kubeConfig        = flag.String("kubeconfig", "", "Path to kubeconfig file with authorization and master location information.")
	ns                = flag.String("namespace", "kube-system", "which namespace the grpc-provider running in kubernetes")
	cm                = flag.String("nodegroup-status-cm", "nodegroup-status", "the config-map name of save nodegroup status")
	nodeGroupConfig   = flag.String("nodegroup-config", "nodegroup-config.yaml", "The path to the nodegroup configuration file.")
	hooksPath         = flag.String("hooks-path", "./hooks", "The path to the hooks, should contains 2 hooks: after_created_hook.sh, before_delete_hook.sh")
	createParallelism = flag.Int("create-parallelism", 5, "Maximum number of concurrent create instance allowed. Limits how many instances can be created simultaneously.")
	deleteParallelism = flag.Int("delete-parallelism", 1, "Maximum number of concurrent delete instance allowed. Limits how many instances can be deleted at the same time.")
)

var grpcServer *grpc.Server

func main() {
	klog.InitFlags(nil)
	klog.Infof("version: %s, gitCommit: %s, buildTime: %s, goVersion: %s", version, gitCommit, buildTime, goVersion)

	leaderElection := defaultLeaderElectionConfiguration()
	options.BindLeaderElectionFlags(&leaderElection, pflag.CommandLine)
	kubeFlag.InitFlags()
	nodegroup.KubeConfigFile = *kubeConfig

	if !leaderElection.LeaderElect {
		run()
	} else {
		id, err := os.Hostname()
		if err != nil {
			klog.Fatalf("Unable to get hostname: %v", err)
		}

		kubeClient := nodegroup.NewKubeClient()
		_, err = kubeClient.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			klog.Fatalf("Failed to get nodes from apiserver: %v", err)
		}

		lock, err := resourcelock.New(
			leaderElection.ResourceLock,
			*ns,
			leaderElection.ResourceName,
			kubeClient.CoreV1(),
			kubeClient.CoordinationV1(),
			resourcelock.ResourceLockConfig{
				Identity: id + "-" + utils.RandStr(10),
			},
		)
		if err != nil {
			klog.Fatalf("Unable to create leader election lock: %v", err)
		}

		leaderelection.RunOrDie(context.TODO(), leaderelection.LeaderElectionConfig{
			Lock:            lock,
			LeaseDuration:   leaderElection.LeaseDuration.Duration,
			RenewDeadline:   leaderElection.RenewDeadline.Duration,
			RetryPeriod:     leaderElection.RetryPeriod.Duration,
			ReleaseOnCancel: true,
			Callbacks: leaderelection.LeaderCallbacks{
				OnStartedLeading: func(_ context.Context) {
					run()
				},
				OnStoppedLeading: func() {
					klog.Fatalf("lost master")
				},
			},
		})
	}
}

func SetupSignalHandler(exit context.CancelFunc) {
	c := make(chan os.Signal, 2)
	signal.Notify(c, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		s := <-c
		klog.Warningf("main received exit signal: %v", s.String())
		grpcServer.GracefulStop()
		exit()
		<-c
		os.Exit(1)
	}()
}

func defaultLeaderElectionConfiguration() componentbaseconfig.LeaderElectionConfiguration {
	return componentbaseconfig.LeaderElectionConfiguration{
		LeaderElect:   true,
		LeaseDuration: metav1.Duration{Duration: defaultLeaseDuration},
		RenewDeadline: metav1.Duration{Duration: defaultRenewDeadline},
		RetryPeriod:   metav1.Duration{Duration: defaultRetryPeriod},
		ResourceLock:  resourcelock.LeasesResourceLock,
		ResourceName:  "cluster-autoscaler-grpc-cloud-provider",
	}
}

func run() {
	grpcServer = grpc.NewServer()
	srv := wrapper.NewCloudProviderGrpcWrapper(
		cloudBuilder.NewCloudProvider(config.AutoscalingOptions{
			CloudProviderName: *cloudProviderFlag,
			CloudConfig:       *cloudConfig,
		}))

	ctx, cancel := context.WithCancel(context.TODO())
	ctx = context.WithValue(ctx, "wg", &sync.WaitGroup{})
	SetupSignalHandler(cancel)

	ngs := nodegroup.GetNodeGroups()
	err := ngs.Run(
		ctx,
		ngs.WithOpsNamespace(*ns),
		ngs.WithOpsStatusConfigMap(*cm),
		ngs.WithOpsConfigFile(*nodeGroupConfig),
		ngs.WithOpsHooksPath(*hooksPath),
		ngs.WithCreateParallelism(*createParallelism),
		ngs.WithDeleteParallelism(*deleteParallelism),
		ngs.CheckKubeNodeSshUser(),
	)
	if err != nil {
		klog.Fatalf("run NodeGroup failed: %s", err)
	}

	go server.HttpServer(ctx, *httpAddress)
	go metrics.Server(*metricsAddress, *metricsPath)

	lis, err := net.Listen("tcp", *grpcAddress)
	if err != nil {
		klog.Fatalf("failed to listen: %s", err)
	}

	// grpc serve
	protos.RegisterCloudProviderServer(grpcServer, srv)
	klog.V(1).Infof("grpc server ready at: %s\n", *grpcAddress)
	if err := grpcServer.Serve(lis); err != nil {
		klog.Fatalf("failed to serve: %v", err)
	}

	common.ContextWaitGroupWait(ctx)
	nodegroup.WriteNodeGroupStatusToConfigMap(context.TODO())
	klog.Warning("main exited")
	os.Exit(0)
}
