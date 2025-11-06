package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	cvm "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cvm/v20170312"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

func main() {
	PatchNodeProviderId()
}

func PatchNodeProviderId() {
	cfg, err := clientcmd.BuildConfigFromFlags("", "kube_config")
	if err != nil {
		klog.Exitf("Error building kubeconfig: %s", err.Error())
		return
	}

	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		klog.Exitf("Error building kubernetes clientset: %s", err.Error())
		return
	}

	nodeList, err := kubeClient.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{
		//LabelSelector: "node-role.kubernetes.io/worker-cvp=ingress",
	})

	if err != nil {
		klog.Exitf("list all nodes failed, %s", err)
	}

	for _, node := range nodeList.Items {
		ip := ""
		for _, addr := range node.Status.Addresses {
			if addr.Type == "InternalIP" {
				ip = addr.Address
			}
		}

		if !strings.HasPrefix(ip, "10.7") {
			continue
		}

		providerId, err := generateNodeProviderId(ip)
		if err != nil {
			klog.Exitf("generateNodeProviderId failed, %s", err)
		}
		//fmt.Printf("%s => %s\n", ip, providerId)
		if node.Spec.ProviderID == providerId {
			continue
		}

		// TODO 需要很慎重，只能中""修改为其他值，一旦设置则不能更改
		node.Spec.ProviderID = providerId
		update, err := kubeClient.CoreV1().Nodes().Update(context.TODO(), &node, metav1.UpdateOptions{})
		if err != nil {
			klog.Exitf("patch node(%s) providerID to %s failed, %s", node.Name, providerId, err)
		}
		fmt.Printf("%s providerId: %s\n", update.Name, update.Spec.ProviderID)
	}
}

func generateNodeProviderId(ip string) (string, error) {
	ak := "xxx"
	sk := "xxx"
	account := "xxx"
	region := "ap-beijing"

	client, err := cvm.NewClient(common.NewCredential(ak, sk), region, profile.NewClientProfile())
	if err != nil {
		return "", fmt.Errorf("fail new cvm client, %s", err)
	}

	filterIP := "private-ip-address"
	req := cvm.NewDescribeInstancesRequest()
	filter := &cvm.Filter{
		Name:   &filterIP,
		Values: []*string{&ip},
	}
	req.Filters = []*cvm.Filter{filter}

	resp, err := client.DescribeInstances(req)
	if err != nil {
		return "", fmt.Errorf("fail to DescribeInstances, %s", err)
	}

	var insId []string
	for _, instance := range resp.Response.InstanceSet {
		for _, addr := range instance.PrivateIpAddresses {
			if *addr == ip {
				insId = append(insId, *instance.InstanceId)
			}
		}
	}

	if len(insId) <= 0 {
		return "", fmt.Errorf("not found instance %s", ip)
	}

	if len(insId) > 1 {
		return "", fmt.Errorf("found too many instance[%v] by %s", insId, ip)
	}

	providerId := fmt.Sprintf("externalgrpc://tencentcloud/%s/%s/%s", account, region, insId[0])
	return providerId, nil
}
