package provider

import (
	"context"
	"fmt"
	"strings"

	. "github.com/sunkaimr/cluster-autoscaler-grpc-cloud-provider/provider/common"
	"github.com/sunkaimr/cluster-autoscaler-grpc-cloud-provider/provider/providers/tencentcloud"
)

var cloudproviderClientCache = make(map[string]Cloudprovider, 10)

type CloudProviderOption struct {
	Accounts          map[string]Provider          `json:"accounts" yaml:"accounts"`
	InstanceParameter map[string]InstanceParameter `json:"instanceParameter" yaml:"instanceParameter"`
}

type Provider map[string]*Credential

type Credential struct {
	SecretId  string `json:"secretId" yaml:"secretId"`
	SecretKey string `json:"secretKey" yaml:"secretKey"`
}

type InstanceParameter struct {
	ProviderIdTemplate string      `json:"providerIdTemplate" yaml:"providerIdTemplate"`
	Parameter          interface{} `json:"parameter" yaml:"parameter"`
}

type Cloudprovider interface {
	CreateInstance(ctx context.Context, para interface{}) (string, error)
	InstanceStatus(ctx context.Context, insId string) (InstanceStatus, error)
	InstancesStatus(ctx context.Context, insIds ...string) (map[string]InstanceStatus, error)
	InstanceIp(ctx context.Context, insId string) (string, error)
	DeleteInstance(ctx context.Context, insId string, para interface{}) error
}

func NewCloudprovider(providerID string, opts CloudProviderOption) (Cloudprovider, error) {
	provider, account, region, _, err := ExtractProviderID(providerID)
	if err != nil {
		return nil, fmt.Errorf("unsupport cloudprovider with '%s', providerID should like 'externalgrpc://<provider>/<account>/<region>/<instanceID>'", providerID)
	}

	genKey := func(provider, account, regin string) string {
		return strings.Join([]string{provider, account, regin}, "/")
	}

	// 从缓存中读取
	if cli, ok := cloudproviderClientCache[genKey(provider, account, region)]; ok {
		return cli, nil
	}

	if opts.Accounts == nil {
		return nil, fmt.Errorf("missing cloudProviderOption config")
	}

	if opts.Accounts[provider] == nil {
		return nil, fmt.Errorf("missing cloudProviderOption.%s option", provider)
	}

	if opts.Accounts[provider][account] == nil {
		return nil, fmt.Errorf("missing cloudProviderOption.%s.%s.secretId|secretKey  option", provider, account)
	}

	paras := make(map[string]string, 4)
	paras["secretId"] = opts.Accounts[provider][account].SecretId
	paras["secretKey"] = opts.Accounts[provider][account].SecretKey
	paras["region"] = region

	switch provider {
	case tencentcloud.ProviderName:
		cli, err := tencentcloud.BuildTencentCloudProvider(paras)
		if err != nil {
			return cli, err
		}
		cloudproviderClientCache[genKey(provider, account, region)] = cli
		return cli, err
	default:
		return nil, fmt.Errorf("unsupport cloudprovider with '%s', providerID should like 'externalgrpc://<provider>/<account>/<region>/<instanceID>'", providerID)
	}
}
