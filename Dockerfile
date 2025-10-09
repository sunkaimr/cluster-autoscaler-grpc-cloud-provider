FROM cowell-images.tencentcloudcr.com/cowellpi/ubuntu:22.04

LABEL maintainer="sunkai"

ENV TZ=Asia/Shanghai
RUN sed -i 's/archive.ubuntu.com/mirrors.aliyun.com/g' /etc/apt/sources.list \
    && ln -snf /usr/share/zoneinfo/$TZ /etc/localtime && echo $TZ > /etc/timezone  \
    && apt-get update \
    && apt-get install -y tzdata dumb-init bash curl \
    && apt-get clean \
    && apt-get autoclean \
    && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/*

COPY cloud-config.cfg /opt/cloud-config.cfg
COPY nodegroup-config.yaml /opt/nodegroup-config.yaml
COPY hooks/* /opt/hooks/
COPY cluster-autoscaler-grpc-cloud-provider /opt/cluster-autoscaler-grpc-cloud-provider
WORKDIR /opt/

ENTRYPOINT ["/usr/bin/dumb-init", "--"]
CMD ["/opt/cluster-autoscaler-grpc-cloud-provider"]