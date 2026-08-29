package test

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	PropName          = "Name"
	PropState         = "State"
	PropReadAhead     = "Read Ahead"
	PropTablesPresent = "Tables present"
	PropOpenCount     = "Open count"
	PropEventNumber   = "Event number"
	PropDevId         = "Major, minor"
	PropTargetsNum    = "Number of targets"
	PropUUID          = "UUID"
)

func checkDevInfo(t *testing.T, got, expected map[string]string) {
	for k, v := range expected {
		require.Equalf(t, v, got[k], "property '%s'", k)
	}
}

func devInfo(name string) (map[string]string, error) {
	out, err := exec.Command("dmsetup", "info", name).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("dminfo(%s): %v\n%s", name, err, out)
	}

	return parseProperties(out)
}

func parseProperties(data []byte) (map[string]string, error) {
	re := regexp.MustCompile(`([\w ,]+):\W*([^\n]+)`)
	matches := re.FindAllStringSubmatch(string(data), -1)

	result := make(map[string]string)
	for _, m := range matches {
		result[m[1]] = m[2]
	}

	return result, nil
}

func waitForFile(filename string) error {
	timeout := 5 * time.Second

	limit := time.Now().Add(timeout)
	for {
		_, err := os.Stat(filename)
		if err == nil {
			return nil // file is here
		}
		if !os.IsNotExist(err) {
			return err
		}
		if time.Now().After(limit) {
			return fmt.Errorf("timeout waiting for file %s", filename)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func randomHex(n int) (string, error) {
	bytes := make([]byte, n)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
