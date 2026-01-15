#!/bin/bash
# 构建包含UI的完整应用程序

set -e

echo "Step 1: Building UI..."
cd ui
npm run build
cd ..

echo "Step 2: Copying UI dist to server directory..."
mkdir -p server/ui
cp -r ui/dist server/ui/

echo "Step 3: Building Go application..."
VER=${VER:-v1.1.0}
APP=${APP:-cluster-autoscaler-grpc-provider}

go build -ldflags "\
   -X 'main.version=${VER}' \
   -X 'main.goVersion=$(go version)' \
   -X 'main.gitCommit=$(git show -s --format=%H)' \
   -X 'main.buildTime=$(date +'%Y-%m-%d %H:%M:%S')'" \
   -o ${APP} main.go

echo "Build completed successfully!"
echo "Binary: ${APP}"
echo "UI files are embedded in the binary"
