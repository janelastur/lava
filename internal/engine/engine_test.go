// Copyright 2023 Adevinta

package engine

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	agentconfig "github.com/adevinta/vulcan-agent/config"
	report "github.com/adevinta/vulcan-report"
	types "github.com/adevinta/vulcan-types"
	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/pkg/archive"
	"github.com/jroimartin/clilog"

	"github.com/adevinta/lava/internal/assettypes"
	"github.com/adevinta/lava/internal/config"
	"github.com/adevinta/lava/internal/containers"
)

var testRuntime containers.Runtime

func TestMain(m *testing.M) {
	flag.Parse()

	level := slog.LevelError
	if testing.Verbose() {
		level = slog.LevelDebug
	}

	h := clilog.NewCLIHandler(os.Stderr, &clilog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(h))

	rt, err := containers.GetenvRuntime()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: get env runtime: %v", err)
		os.Exit(2)
	}
	testRuntime = rt

	os.Exit(m.Run())
}

func TestEngine_Run(t *testing.T) {
	if err := dockerBuild("testdata/engine/lava-engine-test", "lava-engine-test:latest"); err != nil {
		t.Fatalf("could build Docker image: %v", err)
	}

	wantDetails := fmt.Sprintf("lava engine test response %v", rand.Uint64())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, wantDetails)
	}))
	defer srv.Close()

	t.Logf("test server listening at %v", srv.URL)

	var (
		checktypeURLs = []string{"testdata/engine/checktypes_lava_engine_test.json"}
		targets       = []config.Target{
			{
				Identifier: srv.URL,
				AssetType:  types.WebAddress,
			},
		}
		agentConfig = config.AgentConfig{
			PullPolicy: agentconfig.PullPolicyNever,
		}
	)

	eng, err := New(agentConfig, checktypeURLs)
	if err != nil {
		t.Fatalf("engine initialization error: %v", err)
	}
	defer eng.Close()

	engineReport, err := eng.Run(targets)
	if err != nil {
		t.Fatalf("engine run error: %v", err)
	}

	checkReportTarget(t, engineReport, eng.cli.HostGatewayHostname())

	var checkReports []report.Report
	for _, v := range engineReport {
		checkReports = append(checkReports, v)
	}

	if len(checkReports) != 1 {
		t.Fatalf("unexpected number of reports: %v", len(checkReports))
	}

	gotReport := checkReports[0]

	if gotReport.Status != "FINISHED" {
		t.Errorf("unexpected status: %v", gotReport.Status)
	}

	if gotReport.Target != srv.URL {
		t.Errorf("unexpected target: got: %v, want: %v", gotReport.Target, srv.URL)
	}

	if len(gotReport.Vulnerabilities) != 1 {
		t.Fatalf("unexpected number of vulnerabilities: %v", len(gotReport.Vulnerabilities))
	}

	gotDetails := gotReport.Vulnerabilities[0].Details

	if gotDetails != wantDetails {
		t.Errorf("unexpected details: got: %#q, want: %#q", gotDetails, wantDetails)
	}
}

func TestEngine_Run_docker_image(t *testing.T) {
	var (
		checktypeURLs = []string{"testdata/engine/checktypes_trivy.json"}
		targets       = []config.Target{
			{
				Identifier: "python:3.4-alpine",
				AssetType:  types.DockerImage,
			},
		}
		agentConfig = config.AgentConfig{
			PullPolicy: agentconfig.PullPolicyAlways,
		}
	)

	eng, err := New(agentConfig, checktypeURLs)
	if err != nil {
		t.Fatalf("engine initialization error: %v", err)
	}
	defer eng.Close()

	engineReport, err := eng.Run(targets)
	if err != nil {
		t.Fatalf("engine run error: %v", err)
	}

	checkReportTarget(t, engineReport, eng.cli.HostGatewayHostname())

	var checkReports []report.Report
	for _, v := range engineReport {
		checkReports = append(checkReports, v)
	}

	if len(checkReports) != 1 {
		t.Fatalf("unexpected number of reports: %v", len(checkReports))
	}

	gotReport := checkReports[0]

	if gotReport.Status != "FINISHED" {
		t.Errorf("unexpected status: %v", gotReport.Status)
	}

	if len(gotReport.Vulnerabilities) == 0 {
		t.Errorf("no vulnerabilities found")
	}

	t.Logf("found %v vulnerabilities", len(gotReport.Vulnerabilities))
}

func TestEngine_Run_path(t *testing.T) {
	var (
		checktypeURLs = []string{"testdata/engine/checktypes_trivy.json"}
		agentConfig   = config.AgentConfig{
			PullPolicy: agentconfig.PullPolicyAlways,
		}
	)

	tests := []struct {
		name       string
		target     config.Target
		wantStatus string
		wantVulns  bool
	}{
		{
			name: "dir",
			target: config.Target{
				Identifier: "testdata/engine/vulnpath",
				AssetType:  assettypes.Path,
			},
			wantStatus: "FINISHED",
			wantVulns:  true,
		},
		{
			name: "file",
			target: config.Target{
				Identifier: "testdata/engine/vulnpath/Dockerfile",
				AssetType:  assettypes.Path,
			},
			wantStatus: "FINISHED",
			wantVulns:  true,
		},
		{
			name: "not exist",
			target: config.Target{
				Identifier: "testdata/engine/notexist",
				AssetType:  assettypes.Path,
			},
			wantStatus: "FAILED",
			wantVulns:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng, err := New(agentConfig, checktypeURLs)
			if err != nil {
				t.Fatalf("engine initialization error: %v", err)
			}
			defer eng.Close()

			engineReport, err := eng.Run([]config.Target{tt.target})
			if err != nil {
				t.Fatalf("engine run error: %v", err)
			}

			checkReportTarget(t, engineReport, eng.cli.HostGatewayHostname())

			var checkReports []report.Report
			for _, v := range engineReport {
				checkReports = append(checkReports, v)
			}

			if len(checkReports) != 1 {
				t.Fatalf("unexpected number of reports: %v", len(checkReports))
			}

			gotReport := checkReports[0]

			if gotReport.Status != tt.wantStatus {
				t.Errorf("unexpected status: %v", gotReport.Status)
			}

			if (len(gotReport.Vulnerabilities) > 0) != tt.wantVulns {
				t.Errorf("unexpected number of vulnerabilities: %v", len(gotReport.Vulnerabilities))
			}

			t.Logf("found %v vulnerabilities", len(gotReport.Vulnerabilities))
		})
	}
}

func TestEngine_Run_inconclusive(t *testing.T) {
	checktypeURLs := []string{"testdata/engine/checktypes_trivy.json"}
	agentConfig := config.AgentConfig{
		PullPolicy: agentconfig.PullPolicyAlways,
	}
	target := config.Target{
		Identifier: "testdata/engine/vulnpath",
		AssetType:  types.GitRepository,
	}

	eng, err := New(agentConfig, checktypeURLs)
	if err != nil {
		t.Fatalf("engine initialization error: %v", err)
	}
	defer eng.Close()

	engineReport, err := eng.Run([]config.Target{target})
	if err != nil {
		t.Fatalf("engine run error: %v", err)
	}

	checkReportTarget(t, engineReport, eng.cli.HostGatewayHostname())

	var checkReports []report.Report
	for _, v := range engineReport {
		checkReports = append(checkReports, v)
	}

	if len(checkReports) != 1 {
		t.Fatalf("unexpected number of reports: %v", len(checkReports))
	}

	gotReport := checkReports[0]

	if gotReport.Status != "INCONCLUSIVE" {
		t.Errorf("unexpected status: %v", gotReport.Status)
	}

	if len(gotReport.Vulnerabilities) > 0 {
		t.Errorf("unexpected number of vulnerabilities: %v", len(gotReport.Vulnerabilities))
	}
}

func TestEngine_Run_no_jobs(t *testing.T) {
	var (
		checktypeURLs = []string{"testdata/engine/checktypes_lava_engine_test.json"}
		agentConfig   = config.AgentConfig{
			PullPolicy: agentconfig.PullPolicyNever,
		}
	)

	eng, err := New(agentConfig, checktypeURLs)
	if err != nil {
		t.Fatalf("engine initialization error: %v", err)
	}
	defer eng.Close()

	engineReport, err := eng.Run(nil)
	if err != nil {
		t.Fatalf("engine run error: %v", err)
	}

	if len(engineReport) != 0 {
		t.Fatalf("unexpected number of reports: %v", len(engineReport))
	}
}

func dockerBuild(path, tag string) error {
	cli, err := containers.NewDockerdClient(testRuntime)
	if err != nil {
		return fmt.Errorf("new dockerd client: %w", err)
	}
	defer cli.Close()

	tar, err := archive.TarWithOptions(path, &archive.TarOptions{})
	if err != nil {
		return fmt.Errorf("new tar: %w", err)
	}

	opts := dockertypes.ImageBuildOptions{
		Tags:   []string{tag},
		Remove: true,
	}
	resp, err := cli.ImageBuild(context.Background(), tar, opts)
	if err != nil {
		return fmt.Errorf("image build: %w", err)
	}
	defer resp.Body.Close()

	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	return nil
}

// checkReportTarget encodes report as JSON and looks for substr in
// the output. If substr is not found, checkReportTarget calls
// t.Errorf.
func checkReportTarget(t *testing.T, report Report, substr string) {
	doc, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	if strings.Contains(string(doc), substr) {
		t.Errorf("report contains %q:\n%s", substr, doc)
	}
}
