package remote

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bramvdbogaerde/go-scp"
	"golang.org/x/crypto/ssh"
)

// Executer executes commands on remote server, via ssh. Not thread-safe.
type Executer struct {
	user       string
	privateKey string

	conf   *ssh.ClientConfig
	client *ssh.Client
	host   string
}

// NewExecuter creates new Executer instance. It uses user and private key to authenticate.
func NewExecuter(user, privateKey string) (res *Executer, err error) {
	res = &Executer{
		user:       user,
		privateKey: privateKey,
	}

	res.conf, err = res.sshConfig(user, privateKey)
	return res, err
}

// NewExecuters creates multiple new Executer instance. It uses user and private key to authenticate.
func NewExecuters(user, privateKey string, count int) (res []Executer, err error) {
	for i := 0; i < count; i++ {
		var ex *Executer
		ex, err = NewExecuter(user, privateKey)
		if err != nil {
			return nil, err
		}
		res = append(res, *ex)
	}
	return res, err
}

// Connect to remote server using ssh.
func (ex *Executer) Connect(ctx context.Context, host string) (err error) {
	log.Printf("[DEBUG] connect to %s", host)
	ex.client, err = ex.sshClient(ctx, host)
	ex.host = host
	return err
}

// Close connection to remote server.
func (ex *Executer) Close() error {
	if ex.client != nil {
		return ex.client.Close()
	}
	return nil
}

// Run command on remote server.
func (ex *Executer) Run(ctx context.Context, cmd string) (out []string, err error) {
	if ex.client == nil {
		return nil, fmt.Errorf("client is not connected")
	}
	log.Printf("[DEBUG] run %s", cmd)

	return ex.sshRun(ctx, ex.client, cmd)
}

// Upload file to remote server with scp
func (ex *Executer) Upload(ctx context.Context, local, remote string, mkdir bool) (err error) {
	if ex.client == nil {
		return fmt.Errorf("client is not connected")
	}
	log.Printf("[DEBUG] upload %s to %s", local, remote)

	host, port, err := net.SplitHostPort(ex.host)
	if err != nil {
		return fmt.Errorf("failed to split host and port: %w", err)
	}

	req := scpReq{
		client:     ex.client,
		localFile:  local,
		remoteFile: remote,
		mkdir:      mkdir,
		remoteHost: host,
		remotePort: port,
	}
	return ex.scpUpload(ctx, req)
}

// Download file from remote server with scp
func (ex *Executer) Download(ctx context.Context, remote, local string, mkdir bool) (err error) {
	if ex.client == nil {
		return fmt.Errorf("client is not connected")
	}
	log.Printf("[DEBUG] upload %s to %s", local, remote)

	host, port, err := net.SplitHostPort(ex.host)
	if err != nil {
		return fmt.Errorf("failed to split host and port: %w", err)
	}

	req := scpReq{
		client:     ex.client,
		localFile:  local,
		remoteFile: remote,
		mkdir:      mkdir,
		remoteHost: host,
		remotePort: port,
	}
	return ex.scpDownload(ctx, req)
}

// Sync compares local and remote files and uploads unmatched files, recursively.
func (ex *Executer) Sync(ctx context.Context, localDir, remoteDir string) ([]string, error) {
	localFiles, err := ex.getLocalFilesProperties(localDir)
	if err != nil {
		return nil, fmt.Errorf("failed to get local files properties for %s: %w", localDir, err)
	}

	remoteFiles, err := ex.getRemoteFilesProperties(ctx, remoteDir)
	if err != nil {
		return nil, fmt.Errorf("failed to get remote files properties for %s: %w", remoteDir, err)
	}

	unmatchedFiles := ex.findUnmatchedFiles(localFiles, remoteFiles)
	for _, file := range unmatchedFiles {
		localPath := filepath.Join(localDir, file)
		remotePath := filepath.Join(remoteDir, file)
		err := ex.Upload(ctx, localPath, remotePath, true)
		if err != nil {
			return nil, fmt.Errorf("failed to upload %s to %s: %w", localPath, remotePath, err)
		}
		log.Printf("[INFO] synced %s to %s", localPath, remotePath)
	}

	return unmatchedFiles, nil
}

// sshClient creates ssh client connected to remote server. Caller must close session.
func (ex *Executer) sshClient(ctx context.Context, host string) (session *ssh.Client, err error) {
	log.Printf("[DEBUG] create ssh session to %s", host)
	if !strings.Contains(host, ":") {
		host += ":22"
	}

	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, fmt.Errorf("failed to dial: %w", err)
	}

	ncc, chans, reqs, err := ssh.NewClientConn(conn, host, ex.conf)
	if err != nil {
		return nil, fmt.Errorf("failed to create client connection to %s: %v", host, err)
	}
	client := ssh.NewClient(ncc, chans, reqs)
	log.Printf("[DEBUG] ssh session created to %s", host)
	return client, nil
}

// sshRun executes command on remote server. context close sends interrupt signal to remote process.
func (ex *Executer) sshRun(ctx context.Context, client *ssh.Client, command string) (out []string, err error) {
	log.Printf("[DEBUG] run ssh command %q on %s", command, client.RemoteAddr().String())
	session, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	var stdoutBuf bytes.Buffer
	mwr := io.MultiWriter(os.Stdout, &stdoutBuf)
	session.Stdout, session.Stderr = mwr, os.Stderr

	done := make(chan error)
	go func() {
		done <- session.Run(command)
	}()

	select {
	case err = <-done:
		if err != nil {
			return nil, fmt.Errorf("failed to run command on remote server: %w", err)
		}
	case <-ctx.Done():
		err = session.Signal(ssh.SIGINT)
		if err != nil {
			return nil, fmt.Errorf("failed to send interrupt signal to remote process: %w", err)
		}
		return nil, fmt.Errorf("canceled: %w", ctx.Err())
	}

	for _, line := range strings.Split(stdoutBuf.String(), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}

type scpReq struct {
	localFile  string
	remoteHost string
	remotePort string
	remoteFile string
	mkdir      bool
	client     *ssh.Client
}

// scpUpload uploads local file to remote host. Creates remote directory if mkdir is true.
func (ex *Executer) scpUpload(ctx context.Context, req scpReq) error {
	log.Printf("[DEBUG] upload %s to %s:%s", req.localFile, req.remoteHost, req.remoteFile)
	defer func(st time.Time) {
		log.Printf("[INFO] uploaded %s to %s:%s in %s", req.localFile, req.remoteHost, req.remoteFile, time.Since(st))
	}(time.Now())

	if req.mkdir {
		if _, err := ex.sshRun(ctx, req.client, fmt.Sprintf("mkdir -p %s", filepath.Dir(req.remoteFile))); err != nil {
			return fmt.Errorf("failed to create remote directory: %w", err)
		}
	}

	scpClient, err := scp.NewClientBySSH(ex.client)
	if err != nil {
		return fmt.Errorf("failed to create scp client: %v", err)
	}
	defer scpClient.Close()

	inpFh, err := os.Open(req.localFile)
	if err != nil {
		return fmt.Errorf("failed to open local file %s: %v", req.localFile, err)
	}
	defer inpFh.Close() //nolint

	inpFi, err := os.Stat(req.localFile)
	if err != nil {
		return fmt.Errorf("failed to stat local file %s: %v", req.localFile, err)
	}
	log.Printf("[DEBUG] file mode for %s: %s", req.localFile, fmt.Sprintf("%04o", inpFi.Mode().Perm()))

	if err = scpClient.CopyFromFile(ctx, *inpFh, req.remoteFile, fmt.Sprintf("%04o", inpFi.Mode().Perm())); err != nil {
		return fmt.Errorf("failed to copy file: %v", err)
	}

	// set modification time of the uploaded file
	modTime := inpFi.ModTime().In(time.UTC).Format("200601021504.05")
	touchCmd := fmt.Sprintf("touch -m -t %s %s", modTime, req.remoteFile)
	if _, err := ex.sshRun(ctx, req.client, touchCmd); err != nil {
		return fmt.Errorf("failed to set modification time of remote file: %w", err)
	}

	return nil
}

// scpDownload downloads remote file to local path. Creates remote directory if mkdir is true.
func (ex *Executer) scpDownload(ctx context.Context, req scpReq) error {
	log.Printf("[INFO] upload %s to %s:%s", req.localFile, req.remoteHost, req.remoteFile)
	defer func(st time.Time) { log.Printf("[DEBUG] download done for %q in %s", req.localFile, time.Since(st)) }(time.Now())

	if req.mkdir {
		if err := os.MkdirAll(filepath.Dir(req.localFile), 0o750); err != nil {
			return fmt.Errorf("failed to create local directory: %w", err)
		}
	}

	scpClient, err := scp.NewClientBySSH(ex.client)
	if err != nil {
		return fmt.Errorf("failed to create scp client: %v", err)
	}
	defer scpClient.Close()

	outFh, err := os.Create(req.localFile)
	if err != nil {
		return fmt.Errorf("failed to open local file %s: %v", req.localFile, err)
	}
	defer outFh.Close() //nolint

	if err = scpClient.CopyFromRemote(ctx, outFh, req.remoteFile); err != nil {
		return fmt.Errorf("failed to copy file: %v", err)
	}
	return outFh.Sync() //nolint
}

func (ex *Executer) sshConfig(user, privateKeyPath string) (*ssh.ClientConfig, error) {
	key, err := os.ReadFile(privateKeyPath) //nolint
	if err != nil {
		return nil, fmt.Errorf("unable to read private key: %vw", err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("unable to parse private key: %w", err)
	}
	sshConfig := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint
	}

	return sshConfig, nil
}

type fileProperties struct {
	Size     int64
	Time     time.Time
	FileName string
}

// getLocalFilesProperties returns map of file properties for all files in the local directory.
func (ex *Executer) getLocalFilesProperties(dir string) (map[string]fileProperties, error) {
	fileProps := make(map[string]fileProperties)

	// walk local directory and get file properties
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		relPath, err := filepath.Rel(dir, path)
		if err != nil {
			return fmt.Errorf("failed to get relative path: %w", err)
		}
		fileProps[relPath] = fileProperties{Size: info.Size(), Time: info.ModTime(), FileName: info.Name()}
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to walk local directory %s: %w", dir, err)
	}

	return fileProps, nil
}

// getRemoteFilesProperties returns map of file properties for all files in the remote directory.
func (ex *Executer) getRemoteFilesProperties(ctx context.Context, dir string) (map[string]fileProperties, error) {
	checkDirCmd := fmt.Sprintf("test -d %s", dir) // check if directory exists
	if _, checkErr := ex.Run(ctx, checkDirCmd); checkErr != nil {
		return nil, nil
	}

	// find all files in the directory and get their properties
	cmd := fmt.Sprintf("find %s -type f -exec stat -c '%%n:%%s:%%Y' {} \\;", dir) // makes output like: ./file1:1234:1234567890
	output, err := ex.Run(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to get remote files properties: %w", err)
	}

	fileProps := make(map[string]fileProperties)
	for _, line := range output {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("invalid line format: %s", line)
		}

		fullPath := parts[0]
		relPath, err := filepath.Rel(dir, fullPath)
		if err != nil {
			return nil, fmt.Errorf("failed to get relative path for %s: %w", fullPath, err)
		}
		size, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse size for %s: %w", fullPath, err)
		}
		modTimeUnix, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse modification time for %s: %w", fullPath, err)
		}
		modTime := time.Unix(modTimeUnix, 0)
		fileProps[relPath] = fileProperties{Size: size, Time: modTime, FileName: fullPath}
	}

	return fileProps, nil
}

func (ex *Executer) findUnmatchedFiles(local, remote map[string]fileProperties) []string {
	isWithinOneSecond := func(t1, t2 time.Time) bool {
		diff := t1.Sub(t2)
		if diff < 0 {
			diff = -diff
		}
		return diff <= time.Second
	}

	unmatchedFiles := []string{}
	for localPath, localProps := range local {
		remoteProps, exists := remote[localPath]
		if !exists || localProps.Size != remoteProps.Size || !isWithinOneSecond(localProps.Time, remoteProps.Time) {
			unmatchedFiles = append(unmatchedFiles, localPath)
		}
	}
	sort.Slice(unmatchedFiles, func(i, j int) bool {
		return unmatchedFiles[i] < unmatchedFiles[j]
	})
	return unmatchedFiles
}