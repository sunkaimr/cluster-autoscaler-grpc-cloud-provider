/*
Copyright 2022 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"flag"
	nodegroup "github.com/sunkaimr/cluster-autoscaler-grpc-provider/nodegroup"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/sunkaimr/cluster-autoscaler-grpc-provider/wrapper"
	"google.golang.org/grpc"
	cloudBuilder "k8s.io/autoscaler/cluster-autoscaler/cloudprovider/builder"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider/externalgrpc/protos"
	"k8s.io/autoscaler/cluster-autoscaler/config"
	kubeFlag "k8s.io/component-base/cli/flag"
	"k8s.io/klog/v2"
)

var (
	// flags needed by the external grpc provider service
	address           = flag.String("address", ":8086", "The address to expose the grpc service.")
	ns                = flag.String("namespace", "kube-system", "which namespace the  grpc-provider running in kubernetes")
	cm                = flag.String("nodegroup-status-cm", "nodegroup-status", "the config-map name of save nodegroup status")
	cloudProviderFlag = flag.String("cloud-provider", cloudprovider.ExternalGrpcProviderName,
		"Cloud provider type. Available values: ["+strings.Join(cloudBuilder.AvailableCloudProviders, ",")+"]")
	cloudConfig     = flag.String("cloud-config", "", "The path to the cloud provider configuration file.  Empty string for no configuration file.")
	nodeGroupConfig = flag.String("nodegroup-config", "nodegroup-config.yaml", "The path to the nodegroup configuration file.")
)

var grpcServer *grpc.Server

func main() {
	klog.InitFlags(nil)
	kubeFlag.InitFlags()

	grpcServer = grpc.NewServer()
	srv := wrapper.NewCloudProviderGrpcWrapper(
		cloudBuilder.NewCloudProvider(config.AutoscalingOptions{
			CloudProviderName: *cloudProviderFlag,
			CloudConfig:       *cloudConfig,
		}))

	ctx, cancel := context.WithCancel(context.TODO())
	SetupSignalHandler(cancel)

	// 等待node controller运行成功
	ngs := nodegroup.GetNodeGroups()
	err := ngs.Run(
		ctx,
		ngs.WithOpsNamespace(*ns),
		ngs.WithOpsStatusConfigMap(*cm),
		ngs.WithOpsConfigFile(*nodeGroupConfig),
	)
	if err != nil {
		klog.Fatalf("run NodeGroup failed: %s", err)
	}

	lis, err := net.Listen("tcp", *address)
	if err != nil {
		klog.Fatalf("failed to listen: %s", err)
	}

	// serve
	protos.RegisterCloudProviderServer(grpcServer, srv)
	klog.V(1).Infof("Server ready at: %s\n", *address)
	if err := grpcServer.Serve(lis); err != nil {
		klog.Fatalf("failed to serve: %v", err)
	}
}

var onlyOneSignalHandler = make(chan struct{})

func SetupSignalHandler(exit context.CancelFunc) {
	close(onlyOneSignalHandler)
	c := make(chan os.Signal, 2)
	signal.Notify(c, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-c
		klog.Warning("main received exit signal")
		if err := nodegroup.GetNodeGroups().WriteStatusConfigMap(); err != nil {
			klog.Error(err)
		}
		grpcServer.GracefulStop()
		exit()
		<-c
		os.Exit(1)
	}()
}
