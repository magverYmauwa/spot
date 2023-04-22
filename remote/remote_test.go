package remote

import (
	"context"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/go-pkgz/fileutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestExecuter_UploadAndDownload(t *testing.T) {
	ctx := context.Background()
	hostAndPort, teardown := startTestContainer(t)
	defer teardown()

	svc, err := NewExecuter("test", "testdata/test_ssh_key")
	require.NoError(t, err)

	err = svc.Connect(ctx, hostAndPort)
	require.NoError(t, err)
	defer svc.Close()

	err = svc.Upload(ctx, "testdata/data.txt", "/tmp/blah/data.txt", true)
	require.NoError(t, err)

	tmpFile, err := fileutils.TempFileName("", "data.txt")
	require.NoError(t, err)
	defer os.RemoveAll(tmpFile)
	err = svc.Download(ctx, "/tmp/blah/data.txt", tmpFile, true)
	require.NoError(t, err)
	assert.FileExists(t, tmpFile)
	exp, err := os.ReadFile("testdata/data.txt")
	require.NoError(t, err)
	act, err := os.ReadFile(tmpFile)
	require.NoError(t, err)
	assert.Equal(t, string(exp), string(act))
}

func TestExecuter_Upload_FailedNoRemoteDir(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hostAndPort, teardown := startTestContainer(t)
	defer teardown()

	svc, err := NewExecuter("test", "testdata/test_ssh_key")
	require.NoError(t, err)

	err = svc.Connect(ctx, hostAndPort)
	require.NoError(t, err)
	defer svc.Close()

	err = svc.Upload(ctx, "testdata/data.txt", "/tmp/blah/data.txt", false)
	require.EqualError(t, err, "failed to copy file: scp: /tmp/blah/data.txt: No such file or directory\n")
}

func TestExecuter_Upload_CantMakeRemoteDir(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hostAndPort, teardown := startTestContainer(t)
	defer teardown()

	svc, err := NewExecuter("test", "testdata/test_ssh_key")
	require.NoError(t, err)

	err = svc.Connect(ctx, hostAndPort)
	require.NoError(t, err)
	defer svc.Close()

	err = svc.Upload(ctx, "testdata/data.txt", "/dev/blah/data.txt", true)
	require.EqualError(t, err, "failed to create remote directory: failed to run command on remote server: Process exited with status 1")
}

func TestExecuter_Upload_Canceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hostAndPort, teardown := startTestContainer(t)
	defer teardown()

	svc, err := NewExecuter("test", "testdata/test_ssh_key")
	require.NoError(t, err)

	err = svc.Connect(ctx, hostAndPort)
	require.NoError(t, err)
	defer svc.Close()

	cancel()
	err = svc.Upload(ctx, "testdata/data.txt", "/tmp/blah/data.txt", true)
	require.EqualError(t, err, "failed to create remote directory: canceled: context canceled")
}

func TestExecuter_UploadCanceledWithoutMkdir(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hostAndPort, teardown := startTestContainer(t)
	defer teardown()

	svc, err := NewExecuter("test", "testdata/test_ssh_key")
	require.NoError(t, err)

	err = svc.Connect(ctx, hostAndPort)
	require.NoError(t, err)
	defer svc.Close()

	cancel()
	err = svc.Upload(ctx, "testdata/data.txt", "/tmp/blah/data.txt", false)
	require.EqualError(t, err, "failed to copy file: context canceled")
}

func TestExecuter_ConnectCanceled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*100)
	defer cancel()
	hostAndPort, teardown := startTestContainer(t)
	defer teardown()

	svc, err := NewExecuter("test", "testdata/test_ssh_key")
	require.NoError(t, err)

	err = svc.Connect(ctx, hostAndPort)
	require.EqualError(t, err, "failed to dial: dial tcp: lookup localhost: i/o timeout")
}

func TestExecuter_Run(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()
	hostAndPort, teardown := startTestContainer(t)
	defer teardown()

	svc, err := NewExecuter("test", "testdata/test_ssh_key")
	require.NoError(t, err)
	require.NoError(t, svc.Connect(ctx, hostAndPort))

	t.Run("single line out", func(t *testing.T) {
		out, e := svc.Run(ctx, "sh -c 'echo hello world'")
		require.NoError(t, e)
		assert.Equal(t, []string{"hello world"}, out)
	})

	t.Run("multi line out", func(t *testing.T) {
		err = svc.Upload(ctx, "testdata/data.txt", "/tmp/st/data1.txt", true)
		assert.NoError(t, err)
		err = svc.Upload(ctx, "testdata/data.txt", "/tmp/st/data2.txt", true)
		assert.NoError(t, err)

		out, err := svc.Run(ctx, "ls -1 /tmp/st")
		require.NoError(t, err)
		t.Logf("out: %v", out)
		assert.Equal(t, 2, len(out))
		assert.Equal(t, "data1.txt", out[0])
		assert.Equal(t, "data2.txt", out[1])
	})

	t.Run("find out", func(t *testing.T) {
		cmd := fmt.Sprintf("find %s -type f -exec stat -c '%%n:%%s' {} \\;", "/tmp/")
		out, e := svc.Run(ctx, cmd)
		require.NoError(t, e)
		sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
		assert.Equal(t, []string{"/tmp/st/data1.txt:68", "/tmp/st/data2.txt:68"}, out)
	})

}

func TestExecuter_Sync(t *testing.T) {
	ctx := context.Background()
	hostAndPort, teardown := startTestContainer(t)
	defer teardown()

	svc, err := NewExecuter("test", "testdata/test_ssh_key")
	require.NoError(t, err)
	require.NoError(t, svc.Connect(ctx, hostAndPort))

	t.Run("sync", func(t *testing.T) {
		res, err := svc.Sync(ctx, "testdata/sync", "/tmp/sync.dest")
		require.NoError(t, err)
		sort.Slice(res, func(i, j int) bool { return res[i] < res[j] })
		assert.Equal(t, []string{"d1/file11.txt", "file1.txt", "file2.txt"}, res)
		out, e := svc.Run(ctx, "find /tmp/sync.dest -type f -exec stat -c '%s %n' {} \\;")
		require.NoError(t, e)
		sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
		assert.Equal(t, []string{"17 /tmp/sync.dest/d1/file11.txt", "185 /tmp/sync.dest/file1.txt", "61 /tmp/sync.dest/file2.txt"}, out)
	})

	t.Run("sync_again", func(t *testing.T) {
		res, err := svc.Sync(ctx, "testdata/sync", "/tmp/sync.dest")
		require.NoError(t, err)
		assert.Equal(t, 0, len(res), "no files should be synced")
	})

	t.Run("sync no src", func(t *testing.T) {
		_, err = svc.Sync(ctx, "/tmp/no-such-place", "/tmp/sync.dest")
		require.EqualError(t, err, "failed to get local files properties for /tmp/no-such-place: failed to walk local directory"+
			" /tmp/no-such-place: lstat /tmp/no-such-place: no such file or directory")
	})

	// t.Run("sync src is not dir", func(t *testing.T) {
	// 	err = svc.Sync(ctx, "testdata/data.txt", "/tmp/sync.dest")
	// 	require.EqualError(t, err, "failed to walk local directory /tmp/no-such-place: lstat /tmp/no-such-place: no such file or directory")
	// })
}

func TestExecuter_findUnmatchedFiles(t *testing.T) {
	tbl := []struct {
		name     string
		local    map[string]fileProperties
		remote   map[string]fileProperties
		expected []string
	}{
		{
			name: "all files match",
			local: map[string]fileProperties{
				"file1": {Size: 100, Time: time.Unix(0, 0)},
				"file2": {Size: 200, Time: time.Unix(0, 0)},
			},
			remote: map[string]fileProperties{
				"file1": {Size: 100, Time: time.Unix(0, 0)},
				"file2": {Size: 200, Time: time.Unix(0, 0)},
			},
			expected: []string{},
		},
		{
			name: "some files match",
			local: map[string]fileProperties{
				"file1": {Size: 100, Time: time.Unix(0, 0)},
				"file2": {Size: 200, Time: time.Unix(0, 0)},
			},
			remote: map[string]fileProperties{
				"file1": {Size: 100, Time: time.Unix(0, 0)},
				"file2": {Size: 200, Time: time.Unix(1, 1)},
			},
			expected: []string{"file2"},
		},
		{
			name: "no files match",
			local: map[string]fileProperties{
				"file1": {Size: 100, Time: time.Unix(0, 0)},
				"file2": {Size: 200, Time: time.Unix(0, 0)},
			},
			remote: map[string]fileProperties{
				"file1": {Size: 100, Time: time.Unix(1, 1)},
				"file2": {Size: 200, Time: time.Unix(1, 1)},
			},
			expected: []string{"file1", "file2"},
		},
	}

	for _, tc := range tbl {
		t.Run(tc.name, func(t *testing.T) {
			ex := &Executer{}
			result := ex.findUnmatchedFiles(tc.local, tc.remote)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func startTestContainer(t *testing.T) (hostAndPort string, teardown func()) {
	t.Helper()
	ctx := context.Background()
	pubKey, err := os.ReadFile("testdata/test_ssh_key.pub")
	require.NoError(t, err)

	req := testcontainers.ContainerRequest{
		Image:        "lscr.io/linuxserver/openssh-server:latest",
		ExposedPorts: []string{"2222/tcp"},
		WaitingFor:   wait.NewLogStrategy("done.").WithStartupTimeout(time.Second * 30),
		Files: []testcontainers.ContainerFile{
			{HostFilePath: "testdata/test_ssh_key.pub", ContainerFilePath: "/authorized_key"},
		},
		Env: map[string]string{
			"PUBLIC_KEY": string(pubKey),
			"USER_NAME":  "test",
			"TZ":         "Etc/UTC",
		},
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("failed to start container: %v", err)
	}

	_, err = container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "2222")
	require.NoError(t, err)
	return fmt.Sprintf("localhost:%s", port.Port()), func() { container.Terminate(ctx) }
}