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

@REM echo Step 3: Building Go application...
@REM set APP=cluster-autoscaler-grpc-provider.exe
@REM set VER=v1.1.0

@REM for /f "tokens=*" %%i in ('git show -s --format^=%%H') do set GIT_COMMIT=%%i
@REM for /f "tokens=*" %%i in ('go version') do set GO_VERSION=%%i

@REM go build -ldflags "-X 'main.version=%VER%' -X 'main.goVersion=%GO_VERSION%' -X 'main.gitCommit=%GIT_COMMIT%'" -o %APP% main.go

@REM echo Build completed successfully!
@REM echo Binary: %APP%
@REM echo UI files are embedded in the binary
