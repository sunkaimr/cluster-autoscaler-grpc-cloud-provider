package script_executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// ScriptExecutor 用于执行远程脚本
type ScriptExecutor struct {
	client *ssh.Client
}

// NewScriptExecutor 创建新的脚本执行器
func NewScriptExecutor(host, port, user, passwd, keyPath string) (*ScriptExecutor, error) {
	var authMethods []ssh.AuthMethod

	// 优先使用密钥认证
	if keyPath != "" {
		key, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("unable to read private key: %v", err)
		}

		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("unable to parse private key: %v", err)
		}

		authMethods = append(authMethods, ssh.PublicKeys(signer))
	} else {
		authMethods = append(authMethods, ssh.Password(passwd))
	}

	sshConfig := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}

	client, err := ssh.Dial("tcp", host+":"+port, sshConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to dial: %v", err)
	}

	return &ScriptExecutor{client: client}, nil
}

// Close 关闭SSH连接
func (se *ScriptExecutor) Close() error {
	if se.client != nil {
		return se.client.Close()
	}
	return nil
}

// CopyFile 使用SFTP协议复制文件到远程主机
func (se *ScriptExecutor) CopyFile(localPath, remotePath string) error {
	// 创建SFTP客户端
	sftpClient, err := sftp.NewClient(se.client)
	if err != nil {
		return fmt.Errorf("failed to create SFTP client: %v", err)
	}
	defer sftpClient.Close()

	// 创建远程目录（如果不存在）
	remoteDir := filepath.Dir(remotePath)
	if err = sftpClient.MkdirAll(remoteDir); err != nil {
		return fmt.Errorf("failed to create remote directory %s: %v", remoteDir, err)
	}

	// 打开本地文件
	localFile, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("unable to open local file %s: %v", localPath, err)
	}
	defer localFile.Close()

	// 创建远程文件
	remoteFile, err := sftpClient.Create(remotePath)
	if err != nil {
		return fmt.Errorf("unable to create remote file %s: %v", remotePath, err)
	}
	defer remoteFile.Close()

	// 复制文件内容
	if _, err := io.Copy(remoteFile, localFile); err != nil {
		return fmt.Errorf("failed to copy file content: %v", err)
	}
	return nil
}

// RemoveAll 使用SFTP协议远程主机删除相当于'rm -rf xxx'
func (se *ScriptExecutor) RemoveAll(remotePath string) error {
	sftpClient, err := sftp.NewClient(se.client)
	if err != nil {
		return fmt.Errorf("failed to create SFTP client: %v", err)
	}
	defer sftpClient.Close()

	return recursiveRemove(sftpClient, remotePath)
}

// recursiveRemove 递归删除文件或目录
func recursiveRemove(sftpClient *sftp.Client, path string) error {
	info, err := sftpClient.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 文件不存在，直接返回
		}
		return fmt.Errorf("stat failed for %s: %v", path, err)
	}

	if info.IsDir() {
		// 处理目录
		files, err := sftpClient.ReadDir(path)
		if err != nil {
			return fmt.Errorf("read dir failed for %s: %v", path, err)
		}

		for _, file := range files {
			childPath := filepath.Join(path, file.Name())
			if err := recursiveRemove(sftpClient, childPath); err != nil {
				return err
			}
		}
	}

	// 删除文件或空目录
	if err := sftpClient.Remove(path); err != nil {
		return fmt.Errorf("remove failed for %s: %v", path, err)
	}

	return nil
}

// FileExist 使用SFTP协议远程主机指定文件是否存在
func (se *ScriptExecutor) FileExist(remotePath string) (bool, error) {
	sftpClient, err := sftp.NewClient(se.client)
	if err != nil {
		return false, fmt.Errorf("failed to create SFTP client: %v", err)
	}
	defer sftpClient.Close()

	_, err = sftpClient.Stat(remotePath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check remote file %s: %v", remotePath, err)
	}

	return true, nil
}

// WriteFile 使用SFTP协议写入文件到远程主机指定目录
func (se *ScriptExecutor) WriteFile(reader io.Reader, remotePath string) error {
	sftpClient, err := sftp.NewClient(se.client)
	if err != nil {
		return fmt.Errorf("failed to create SFTP client: %v", err)
	}
	defer sftpClient.Close()

	remoteDir := filepath.Dir(remotePath)
	if err = sftpClient.MkdirAll(remoteDir); err != nil {
		return fmt.Errorf("failed to create remote directory %s: %v", remoteDir, err)
	}

	remoteFile, err := sftpClient.Create(remotePath)
	if err != nil {
		return fmt.Errorf("unable to create remote file %s: %v", remotePath, err)
	}
	defer remoteFile.Close()

	if _, err := io.Copy(remoteFile, reader); err != nil {
		return fmt.Errorf("write remote file failed: %v", err)
	}
	return nil
}

// RunCommand 在远程主机上执行命令并返回输出
func (se *ScriptExecutor) RunCommand(ctx context.Context, cmd string) (string, error) {
	session, err := se.client.NewSession()
	if err != nil {
		return "", fmt.Errorf("failed to create session: %v", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	resultChan := make(chan error, 1)

	go func() {
		resultChan <- session.Run(cmd)
	}()

	select {
	case <-ctx.Done():
		session.Signal(ssh.SIGKILL)
		return "", fmt.Errorf("command execution timed out or canceled: %v", ctx.Err())
	case err = <-resultChan:
		output := stdout.String()
		if err != nil {
			return output, fmt.Errorf("command failed: %v, stderr: %s", err, stderr.String())
		}
		return output, nil
	}
}

// ExecuteScript 执行远程脚本并捕获输出
func (se *ScriptExecutor) ExecuteScript(ctx context.Context, scriptPath, logPath string) (string, error) {
	//cmd := fmt.Sprintf("%s 2>&1 | tee %s", scriptPath, logPath)
	cmd := fmt.Sprintf("set -o pipefail; %s 2>&1 | tee %s", scriptPath, logPath)
	output, err := se.RunCommand(ctx, cmd)
	if err != nil {
		return output, fmt.Errorf("script execution failed: %v", err)
	}
	return output, nil
}
