package common

import (
	"fmt"
	"strings"

	. "github.com/sunkaimr/cluster-autoscaler-grpc-provider/nodegroup/instance"
	"k8s.io/klog/v2"
)

// ExtractProviderID providerID格式如下：externalgrpc://<provider>/<account>/<region>/<instanceID>
func ExtractProviderID(providerID string) (provider, account, region, instanceID string, err error) {
	if !strings.HasPrefix(providerID, "externalgrpc://") {
		return "", "", "", "",
			fmt.Errorf("unsupport provider format: '%s', "+
				"only support like this: 'externalgrpc://<provider>/<account>/<region>/<instanceID>'", providerID)
	}
	ss := strings.Split(strings.TrimPrefix(providerID, "externalgrpc://"), "/")
	switch len(ss) {
	case 0:
		return "", "", "", "",
			fmt.Errorf("provider,account,regin,instanceID not define")
	case 1:
		return ss[0], "", "", "",
			fmt.Errorf("account,regin,instanceID not define")
	case 2:
		return ss[0], ss[1], "", "",
			fmt.Errorf("regin,instanceID not define")
	case 3:
		return ss[0], ss[1], ss[2], "",
			fmt.Errorf("instanceID not define")
	case 4:
		return ss[0], ss[1], ss[2], ss[3], nil
	default:
		return ss[0], ss[1], ss[2], ss[3], nil
	}
}

func GenerateInstanceProviderID(provider, account, region, instanceID string) string {
	return fmt.Sprintf("externalgrpc://%s/%s/%s/%s", provider, account, region, instanceID)
}

func ClassifiedInstancesByProviderID(ins []*Instance) map[string][]*Instance {
	genKey := func(provider, account, regin string) string {
		return strings.Join([]string{provider, account, regin}, "/")
	}

	classifiedIns := make(map[string][]*Instance, len(ins))
	for _, i := range ins {
		provider, account, regin, _, err := ExtractProviderID(i.ProviderID)
		if err != nil {
			klog.Warningf("extract %s providerID failed, %s", i.Name, err)
			continue
		}
		key := genKey(provider, account, regin)
		classifiedIns[key] = append(classifiedIns[key], i)
	}
	return classifiedIns
}
