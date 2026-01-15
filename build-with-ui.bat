@echo off
REM 构建包含UI的完整应用程序 (Windows版本)

echo Step 1: Building UI...
cd ui
call npm run build
if %errorlevel% neq 0 exit /b %errorlevel%
cd ..

echo Step 2: Copying UI dist to server directory...
if not exist server\ui mkdir server\ui
xcopy /E /I /Y ui\dist server\ui\dist

echo Step 3: Building Go application...
set APP=cluster-autoscaler-grpc-provider.exe
set VER=v1.1.0

for /f "tokens=*" %%i in ('git show -s --format^=%%H') do set GIT_COMMIT=%%i
for /f "tokens=*" %%i in ('go version') do set GO_VERSION=%%i

go build -ldflags "-X 'main.version=%VER%' -X 'main.goVersion=%GO_VERSION%' -X 'main.gitCommit=%GIT_COMMIT%'" -o %APP% main.go

echo Build completed successfully!
echo Binary: %APP%
echo UI files are embedded in the binary
