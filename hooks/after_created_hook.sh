#!/bin/bash

set -e

# 会将env文件中内置的环境变量注入此脚本
node_name=${NODE_NAME}
node_ip=${NODE_IP}
provider_id=${PROVIDER_ID}


# 日志函数
log_info() {
    echo "$(date '+%Y-%m-%d %H:%M:%S') [INFO] $1"
}

log_warn() {
    echo "$(date '+%Y-%m-%d %H:%M:%S') [WARN] $1"
}

log_error() {
    echo "$(date '+%Y-%m-%d %H:%M:%S') [ERROR] $1"
}


# 设置主机名
set_hostname() {
    local current_hostname=$(hostname)
    if [ "$current_hostname" != "${node_name}" ]; then
        log_info "Setting hostname to ${node_name}"
        sudo /usr/bin/hostnamectl set-hostname "${node_name}"
        log_info "Hostname set successfully"
    else
        log_info "Hostname is already set to ${node_name}, skipping"
    fi
}

# 配置Hosts文件
configure_hosts() {
    local hosts_lines=(
        "127.0.0.1 ${node_name}"
        "10.2.13.10 kubernetes.default.svc"
        "10.8.16.99 docker-repo.gaojihealth.cn"
        "10.8.51.100 rancher.cowelltech.com"
        "10.2.6.3 cowell-images.tencentcloudcr.com"
    )

    for line in "${hosts_lines[@]}"; do
        if ! grep -q "^${line}" /etc/hosts; then
            log_info "Adding '$line' to /etc/hosts"
            echo "$line" | sudo tee -a /etc/hosts > /dev/null
        else
            log_info "'$line' already exists in /etc/hosts, skipping"
        fi
    done
    log_info "Hosts configuration completed"
}

# 安装Kubernetes组件
install_k8s_components() {
    local version="1.18.19-00"
    local packages=("kubeadm" "kubectl" "kubelet")

    log_info "Updating package list"
    sudo apt-get update

    for pkg in "${packages[@]}"; do
        if dpkg -l | grep -q "ii  $pkg"; then
            local current_version=$(dpkg -s $pkg 2>/dev/null | grep Version | awk '{print $2}' || echo "")
            if [ "$current_version" != "$version" ]; then
                log_info "Reinstalling $pkg (current: $current_version, desired: $version)"
                sudo apt-get install -y --allow-downgrades "$pkg=$version"
            else
                log_info "$pkg is already installed with correct version $version"
            fi
        else
            log_info "Installing $pkg=$version"
            sudo apt-get install -y "$pkg=$version"
        fi
    done
    log_info "Kubernetes components installation completed"
}

set_kubelet_provider_id() {
    echo "KUBELET_EXTRA_ARGS=\"--provider-id=${provider_id}\"" | sudo tee /etc/default/kubelet > /dev/null
    log_info "Adding KUBELET_EXTRA_ARGS to /etc/default/kubelet"
}

# 加入Kubernetes集群
join_cluster() {
    log_info "Resetting kubeadm and joining cluster"
    yes | sudo kubeadm reset 2>/dev/null || true

    # 加入集群
    log_info "Joining Kubernetes cluster"
    sudo kubeadm join kubernetes.default.svc:6443 \
        --token 6qeuu1.o73nys8e313uabkb \
        --discovery-token-ca-cert-hash sha256:f6123a5713c200d94053234a26c17389d8629d9f0b56a238514aa66eeb5aeeb4

    log_info "Successfully joined Kubernetes cluster"
}

add_host_to_cmdb(){
    local bk_user="kubernetes"
    local uni_resource_name="Saas-Node-$(echo $node_ip | tr \. \_)"
    local bk_create=$(date "+%Y-%m-%d")
    local bk_env="1"
    local bk_func="kubernetes"

    local request_data=$(cat << EOF
          {
              "bk_supplier_id": 0,
              "bk_biz_id": 19,
              "bk_app_code": "saas-sops",
              "bk_app_secret": "24eb30a5-1000-4d8a-9e56-1adde91fd825",
              "bk_username": "saas-sops",
              "host_info": {
                  "8": {
                      "uni_resource_name": "${uni_resource_name}",
                      "bk_host_innerip": "${node_ip}",
                      "bk_cloud_id": 8,
                      "bk_create": "${bk_create}",
                      "bk_over": "2666-06-06",
                      "bk_env": "${bk_env}",
                      "bk_func": "${bk_func}",
                      "bk_user": "${bk_user}",
                      "bk_os_type": "1"
                  }
              }
          }
EOF
)

    log_info "send request to cmdb: ${request_data}"

    local response
    response=$(curl -sS -X POST \
                --insecure \
                --connect-timeout 30 \
                --max-time 60 \
                --header "Content-Type: application/json" \
                --data "${request_data}" \
                --resolve paas.cowelltech.com:443:10.8.8.20 \
                'https://paas.cowelltech.com/api/c/compapi/v2/cc/add_host_to_resource/' 2>&1)
    local exit_code=$?

    if [[ ${exit_code} -eq 0 ]]; then
        log_info "add ${node_ip} to cmdb successfully"
    else
        log_error "add ${node_ip} to cmdb failed, code: ${exit_code}"
        log_error "error response: ${response}"
    fi
}

# 主函数
main() {
    log_info "starting exec after_created_hook script"

    # 系统刚起来等待10s中稳定
    sleep 10
    
    set_hostname
    configure_hosts
    #install_k8s_components
    set_kubelet_provider_id
    join_cluster
    add_host_to_cmdb

    log_info "exec after_created_hook script successfully"
}

main