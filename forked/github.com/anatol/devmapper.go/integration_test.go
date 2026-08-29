package devmapper

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/anatol/vmtest"
	"github.com/stretchr/testify/require"
	"github.com/tmc/scp"
	"golang.org/x/crypto/ssh"
)

func TestWithQemu(t *testing.T) {
	t.Parallel()

	wd, err := os.Getwd()
	require.NoError(t, err)

	cmd := exec.Command("go", "test", "-c", "-o", "devmapper.go.test")
	cmd.Dir = filepath.Join(wd, "examples")
	if testing.Verbose() {
		log.Print("compile in-qemu test binary")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	require.NoError(t, cmd.Run())
	defer os.Remove("examples/devmapper.go.test")

	// These integration tests use QEMU with a statically-compiled kernel (to avoid inintramfs) and a specially
	// prepared rootfs. See [instructions](https://github.com/anatol/vmtest/blob/master/docs/prepare_image.md)
	// how to prepare these binaries.
	params := []string{"-net", "user,hostfwd=tcp::10022-:22", "-net", "nic", "-m", "8G", "-smp", strconv.Itoa(runtime.NumCPU())}
	if os.Getenv("TEST_DISABLE_KVM") != "1" {
		params = append(params, "-enable-kvm", "-cpu", "host")
	}
	opts := vmtest.QemuOptions{
		OperatingSystem: vmtest.OS_LINUX,
		Kernel:          "bzImage",
		Params:          params,
		Disks:           []vmtest.QemuDisk{{Path: "rootfs.cow", Format: "qcow2"}},
		Append:          []string{"root=/dev/sda", "rw"},
		Verbose:         testing.Verbose(),
		Timeout:         3 * time.Minute,
	}
	// Run QEMU instance
	qemu, err := vmtest.NewQemu(&opts)
	require.NoError(t, err)
	// Shutdown QEMU at the end of the test case
	defer qemu.Shutdown()

	config := &ssh.ClientConfig{
		User:            "root",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	conn, err := ssh.Dial("tcp", "localhost:10022", config)
	require.NoError(t, err)
	defer conn.Close()

	sess, err := conn.NewSession()
	require.NoError(t, err)
	defer sess.Close()

	scpSess, err := conn.NewSession()
	require.NoError(t, err)

	require.NoError(t, scp.CopyPath("examples/devmapper.go.test", "devmapper.go.test", scpSess))

	testCmd := "./devmapper.go.test"
	if testing.Verbose() {
		testCmd += " -test.v"
	}

	output, err := sess.CombinedOutput(testCmd)
	if testing.Verbose() {
		fmt.Print(string(output))
	}
	require.NoError(t, err)
}
