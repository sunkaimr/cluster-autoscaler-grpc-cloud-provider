package main

import (
	"context"
	"flag"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/sunkaimr/cluster-autoscaler-grpc-provider/nodegroup"
	"github.com/sunkaimr/cluster-autoscaler-grpc-provider/pkg/common"
	"github.com/sunkaimr/cluster-autoscaler-grpc-provider/wrapper"
	"google.golang.org/grpc"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	cloudBuilder "k8s.io/autoscaler/cluster-autoscaler/cloudprovider/builder"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider/externalgrpc/protos"
	"k8s.io/autoscaler/cluster-autoscaler/config"
	kubeFlag "k8s.io/component-base/cli/flag"
	"k8s.io/klog/v2"
)

var (
	// flags needed by the external grpc provider service
	address           = flag.String("address", ":8086", "The address to expose the grpc service.")
	cloudProviderFlag = flag.String("cloud-provider", cloudprovider.ExternalGrpcProviderName, "cloud provider type, only support 'externalgrpc'")
	cloudConfig       = flag.String("cloud-config", "cloud-config.cfg", "The path to the cloud provider configuration file.")
	kubeConfig        = flag.String("kubeconfig", "", "Path to kubeconfig file with authorization and master location information.")
	ns                = flag.String("namespace", "kube-system", "which namespace the grpc-provider running in kubernetes")
	cm                = flag.String("nodegroup-status-cm", "nodegroup-status", "the config-map name of save nodegroup status")
	nodeGroupConfig   = flag.String("nodegroup-config", "nodegroup-config.yaml", "The path to the nodegroup configuration file.")
	hooksPath         = flag.String("hooks-path", "./hooks", "The path to the hooks, should contains 2 hooks: after_created_hook.sh, before_delete_hook.sh")
	createParallelism = flag.Int("create-parallelism", 1, "Maximum number of concurrent create instance allowed. Limits how many instances can be created simultaneously.")
	deleteParallelism = flag.Int("delete-parallelism", 1, "Maximum number of concurrent delete instance allowed. Limits how many instances can be deleted at the same time.")
)

var (
	gitCommit string
	buildTime string
	goVersion string
	version   string
)

var grpcServer *grpc.Server

func main() {
	klog.InitFlags(nil)
	klog.Infof("version: %s ,gitCommit: %s, buildTime :%s, goVersion :%s", version, gitCommit, buildTime, goVersion)
	kubeFlag.InitFlags()

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
		ngs.WithOpsKubeConfig(*kubeConfig),
		ngs.WithOpsConfigFile(*nodeGroupConfig),
		ngs.WithOpsHooksPath(*hooksPath),
		ngs.WithCreateParallelism(*createParallelism),
		ngs.WithDeleteParallelism(*deleteParallelism),
		ngs.CheckKubeNodeSshUser(),
	)
	if err != nil {
		klog.Fatalf("run NodeGroup failed: %s", err)
	}

	lis, err := net.Listen("tcp", *address)
	if err != nil {
		klog.Fatalf("failed to listen: %s", err)
	}

	// grpc serve
	protos.RegisterCloudProviderServer(grpcServer, srv)
	klog.V(1).Infof("Server ready at: %s\n", *address)
	if err := grpcServer.Serve(lis); err != nil {
		klog.Fatalf("failed to serve: %v", err)
	}

	common.ContextWaitGroupWait(ctx)
	nodegroup.WriteNodeGroupStatusToConfigMap(context.TODO())
	klog.Warning("main exited")
}

func SetupSignalHandler(exit context.CancelFunc) {
	c := make(chan os.Signal, 2)
	signal.Notify(c, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		s := <-c
		klog.Warningf("main received exit signal(%v)", s.String())
		grpcServer.GracefulStop()
		exit()
		<-c
		os.Exit(1)
	}()
}
