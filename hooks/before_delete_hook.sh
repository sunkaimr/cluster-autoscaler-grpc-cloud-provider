#!/bin/bash

set -e


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


# 主函数
main() {
    log_info "starting exec before_delete_hook script"


    log_info "exec before_delete_hook script successfully"
}

main