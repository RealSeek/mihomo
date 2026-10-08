//go:build windows && mihomo_integration && !no_easytier

package outbound

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
)

type easyTierWebTestServer struct {
	ctx        context.Context
	executable string
	directory  string
	apiURL     string
	endpoint   string
	auth       string
	configPort int
	apiPort    int
	client     *http.Client
	process    *exec.Cmd
	log        *os.File
}

func newEasyTierWebTestServer(t *testing.T, ctx context.Context, prefix string) *easyTierWebTestServer {
	t.Helper()
	executable := os.Getenv("EASYTIER_WEB_E2E_SERVER_EXECUTABLE")
	if executable == "" {
		t.Fatal("EASYTIER_WEB_E2E_SERVER_EXECUTABLE must identify the existing official v2.7.0 portable server")
	}
	executable, err := filepath.Abs(executable)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(executable); err != nil || info.IsDir() {
		t.Fatalf("official server executable is unavailable: %v", err)
	}
	directory, err := os.MkdirTemp(filepath.Dir(executable), prefix)
	if err != nil {
		t.Fatal(err)
	}
	configAddress, configPort := easyTierWebTestAddress(t)
	apiAddress, apiPort := easyTierWebTestAddress(t)
	server := &easyTierWebTestServer{
		ctx: ctx, executable: executable, directory: directory,
		apiURL:   "http://" + apiAddress,
		endpoint: "tcp://" + configAddress + "/" + uuid.Must(uuid.NewV4()).String(),
		auth:     uuid.Must(uuid.NewV4()).String(), configPort: configPort, apiPort: apiPort,
		client: &http.Client{Timeout: 5 * time.Second},
	}
	t.Cleanup(func() {
		server.stop(t)
		if err := os.RemoveAll(directory); err != nil {
			t.Errorf("remove owned configuration-server directory: %v", err)
		}
	})
	return server
}

func easyTierWebTestAddress(t *testing.T) (string, int) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address, port := listener.Addr().String(), listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address, port
}

func (s *easyTierWebTestServer) start(t *testing.T) {
	t.Helper()
	var err error
	s.log, err = os.OpenFile(filepath.Join(s.directory, "server.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	s.process = exec.Command(s.executable,
		"--db", "sqlite:./test.db", "--config-server-protocol", "tcp", "--config-server-port", fmt.Sprint(s.configPort),
		"--api-server-addr", "127.0.0.1", "--api-server-port", fmt.Sprint(s.apiPort), "--internal-auth-token", s.auth,
		"--allow-auto-create-user", "--disable-registration", "--console-log-level", "warn")
	s.process.Dir, s.process.SysProcAttr = s.directory, &syscall.SysProcAttr{HideWindow: true}
	s.process.Stdout, s.process.Stderr = s.log, s.log
	if err := s.process.Start(); err != nil {
		_ = s.log.Close()
		s.log = nil
		t.Fatalf("start owned configuration server: %v", err)
	}
}

func (s *easyTierWebTestServer) stop(t *testing.T) {
	t.Helper()
	if s.process != nil && s.process.Process != nil {
		if err := s.process.Process.Kill(); err != nil && err != os.ErrProcessDone {
			t.Errorf("stop owned configuration server: %v", err)
		}
		_ = s.process.Wait()
		s.process = nil
	}
	if s.log != nil {
		_ = s.log.Close()
		s.log = nil
	}
}

func (s *easyTierWebTestServer) request(method, path string, payload any) ([]byte, int, error) {
	var body []byte
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			return nil, 0, err
		}
	}
	req, err := http.NewRequestWithContext(s.ctx, method, s.apiURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("X-Internal-Auth", s.auth)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := s.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	body, err = io.ReadAll(response.Body)
	return body, response.StatusCode, err
}
