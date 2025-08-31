package script_executor

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// SSHConfig 包含SSH连接配置
type SSHConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	KeyPath  string // 可选，如果使用密钥认证
}

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
		Timeout:         30 * time.Second,
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
	if err := sftpClient.MkdirAll(remoteDir); err != nil {
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

	// 设置文件权限（可选）
	//if err := sftpClient.Chmod(remotePath, 0644); err != nil {
	//	return fmt.Errorf("failed to set file permissions: %v", err)
	//}

	return nil
}

// RunCommand 在远程主机上执行命令并返回输出
func (se *ScriptExecutor) RunCommand(cmd string) (string, error) {
	session, err := se.client.NewSession()
	if err != nil {
		return "", fmt.Errorf("failed to create session: %v", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	err = session.Run(cmd)
	output := stdout.String()
	if err != nil {
		return output, fmt.Errorf("command failed: %v, stderr: %s", err, stderr.String())
	}

	return output, nil
}

// ExecuteScript 执行远程脚本并捕获输出
func (se *ScriptExecutor) ExecuteScript(scriptPath string) (string, error) {
	// 确保脚本有执行权限
	chmodCmd := fmt.Sprintf("chmod +x %s", scriptPath)
	if _, err := se.RunCommand(chmodCmd); err != nil {
		return "", fmt.Errorf("failed to make script executable: %v", err)
	}

	// 执行脚本并将输出重定向到日志文件，同时捕获输出
	logPath := "/opt/kube-node.log"
	cmd := fmt.Sprintf("%s 2>&1 | tee %s", scriptPath, logPath)

	output, err := se.RunCommand(cmd)
	if err != nil {
		return output, fmt.Errorf("script execution failed: %v", err)
	}

	return output, nil
}
