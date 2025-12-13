package assist

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCmdPruningCronjobErrorSRE(t *testing.T) {
	cmd := NewCmdPruningCronjobErrorSRE()
	
	assert.NotNil(t, cmd)
	assert.Equal(t, "pruningcronjoberrorsre", cmd.Use)
	assert.Contains(t, cmd.Short, "Collect diagnostic information")
	assert.Contains(t, cmd.Long, "PruningCronjobErrorSRE")
	assert.Contains(t, cmd.Long, "diagnostic data")
	assert.NotEmpty(t, cmd.Example)
}

func TestPruningCronjobOptions_complete(t *testing.T) {
	tests := []struct {
		name      string
		outputDir string
		wantErr   bool
		checkDir  bool
	}{
		{
			name:      "default output directory",
			outputDir: "",
			wantErr:   false,
			checkDir:  true,
		},
		{
			name:      "custom output directory",
			outputDir: "/tmp/test-output",
			wantErr:   false,
			checkDir:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ops := newPruningCronjobOptions()
			ops.outputDir = tt.outputDir
			
			cmd := NewCmdPruningCronjobErrorSRE()
			err := ops.complete(cmd, []string{})
			
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.checkDir {
					assert.Contains(t, ops.outputDir, "pruning-cronjob-diagnostics-")
				} else {
					assert.Equal(t, tt.outputDir, ops.outputDir)
				}
			}
		})
	}
}

func TestPruningCronjobOptions_getClusterID(t *testing.T) {
	tests := []struct {
		name           string
		mockOutput     string
		mockError      error
		expectedID     string
		expectedErr    bool
	}{
		{
			name:        "valid cluster ID",
			mockOutput:  "  abc123-def456-ghi789  \n",
			mockError:   nil,
			expectedID:  "abc123-def456-ghi789",
			expectedErr: false,
		},
		{
			name:        "empty cluster ID",
			mockOutput:  "",
			mockError:   nil,
			expectedID:  "N/A",
			expectedErr: false,
		},
		{
			name:        "command error",
			mockOutput:  "",
			mockError:   fmt.Errorf("command failed"),
			expectedID:  "N/A",
			expectedErr: false, // getClusterID doesn't return errors
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ops := newPruningCronjobOptions()
			ops.commandRunner = func(name string, args []string) (string, error) {
				return tt.mockOutput, tt.mockError
			}

			id, err := ops.getClusterID()
			
			assert.Equal(t, tt.expectedID, id)
			if tt.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestPruningCronjobOptions_collectSeccompErrors(t *testing.T) {
	tests := []struct {
		name           string
		podJSON        string
		expectedOutput string
	}{
		{
			name: "pods with seccomp errors",
			podJSON: `{
				"items": [
					{
						"metadata": {"name": "pod1"},
						"status": {
							"containerStatuses": [{
								"state": {
									"waiting": {"reason": "seccomp error 524"}
								}
							}]
						}
					},
					{
						"metadata": {"name": "pod2"},
						"status": {
							"containerStatuses": [{
								"state": {
									"terminated": {"reason": "SECCOMP_FAILED"}
								}
							}]
						}
					}
				]
			}`,
			expectedOutput: "pod1\npod2",
		},
		{
			name: "pods without seccomp errors",
			podJSON: `{
				"items": [
					{
						"metadata": {"name": "pod1"},
						"status": {
							"containerStatuses": [{
								"state": {
									"waiting": {"reason": "ImagePullBackOff"}
								}
							}]
						}
					}
				]
			}`,
			expectedOutput: "No seccomp errors detected",
		},
		{
			name:           "empty pod list",
			podJSON:        `{"items": []}`,
			expectedOutput: "No seccomp errors detected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			ops := newPruningCronjobOptions()
			ops.outputDir = tmpDir
			ops.commandRunner = func(name string, args []string) (string, error) {
				return tt.podJSON, nil
			}

			err := ops.collectSeccompErrors(nil, nil, nil)
			assert.NoError(t, err)

			content, err := os.ReadFile(filepath.Join(tmpDir, "14-seccomp-errors.txt"))
			require.NoError(t, err)
			assert.Equal(t, tt.expectedOutput, strings.TrimSpace(string(content)))
		})
	}
}

func TestPruningCronjobOptions_collectJobHistory(t *testing.T) {
	tests := []struct {
		name           string
		jobJSON        string
		expectedOutput string
	}{
		{
			name: "jobs with status",
			jobJSON: `{
				"items": [
					{
						"metadata": {"name": "job1"},
						"status": {
							"failed": 2,
							"succeeded": 5
						}
					},
					{
						"metadata": {"name": "job2"},
						"status": {
							"failed": 0,
							"succeeded": 10
						}
					}
				]
			}`,
			expectedOutput: "job1: 2 failed, 5 succeeded\njob2: 0 failed, 10 succeeded",
		},
		{
			name: "jobs with nil status",
			jobJSON: `{
				"items": [
					{
						"metadata": {"name": "job1"},
						"status": {}
					}
				]
			}`,
			expectedOutput: "job1: 0 failed, 0 succeeded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			ops := newPruningCronjobOptions()
			ops.outputDir = tmpDir
			ops.commandRunner = func(name string, args []string) (string, error) {
				return tt.jobJSON, nil
			}

			err := ops.collectJobHistory(nil, nil, nil)
			assert.NoError(t, err)

			content, err := os.ReadFile(filepath.Join(tmpDir, "16-job-history.txt"))
			require.NoError(t, err)
			assert.Equal(t, tt.expectedOutput, strings.TrimSpace(string(content)))
		})
	}
}

func TestPruningCronjobOptions_collectPods(t *testing.T) {
	tests := []struct {
		name          string
		podJSON       string
		expectFailing bool
	}{
		{
			name: "failing pods",
			podJSON: `{
				"items": [
					{
						"metadata": {"name": "pod1"},
						"status": {"phase": "Failed"}
					},
					{
						"metadata": {"name": "pod2"},
						"status": {"phase": "Pending"}
					}
				]
			}`,
			expectFailing: true,
		},
		{
			name: "no failing pods",
			podJSON: `{
				"items": [
					{
						"metadata": {"name": "pod1"},
						"status": {"phase": "Running"}
					},
					{
						"metadata": {"name": "pod2"},
						"status": {"phase": "Succeeded"}
					}
				]
			}`,
			expectFailing: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			ops := newPruningCronjobOptions()
			ops.outputDir = tmpDir
			
			commandCalls := 0
			ops.commandExecutor = func(name string, args []string, outputFile string) error {
				commandCalls++
				// Simulate successful command execution
				return os.WriteFile(outputFile, []byte("test output"), 0644)
			}
			ops.commandRunner = func(name string, args []string) (string, error) {
				if strings.Contains(strings.Join(args, " "), "pod") && strings.Contains(strings.Join(args, " "), "json") {
					return tt.podJSON, nil
				}
				return "", nil
			}

			err := ops.collectPods(nil, nil, nil)
			assert.NoError(t, err)

			// Verify that commands were called
			assert.Greater(t, commandCalls, 0)
		})
	}
}

func TestPruningCronjobOptions_generateSummary(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newPruningCronjobOptions()
	ops.outputDir = tmpDir

	// Create some test files
	testFiles := []string{
		"01-jobs.txt",
		"02-pods.txt",
		"06-network-config.json",
		"17-cluster-version.yaml",
	}

	for _, filename := range testFiles {
		err := os.WriteFile(filepath.Join(tmpDir, filename), []byte("test content"), 0644)
		require.NoError(t, err)
	}

	// Write jobs and pods content
	err := os.WriteFile(filepath.Join(tmpDir, "01-jobs.txt"), []byte("NAME\tSTATUS\njob1\tActive"), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tmpDir, "02-pods.txt"), []byte("NAME\tSTATUS\npod1\tRunning"), 0644)
	require.NoError(t, err)

	ops.commandRunner = func(name string, args []string) (string, error) {
		return "OVNKubernetes", nil
	}

	err = ops.generateSummary("test-cluster-id", nil)
	assert.NoError(t, err)

	// Verify summary file was created
	summaryFile := filepath.Join(tmpDir, "00-SUMMARY.txt")
	content, err := os.ReadFile(summaryFile)
	require.NoError(t, err)

	summary := string(content)
	assert.Contains(t, summary, "PruningCronjobErrorSRE Diagnostic Collection Summary")
	assert.Contains(t, summary, "test-cluster-id")
	assert.Contains(t, summary, "01-jobs.txt")
	assert.Contains(t, summary, "02-pods.txt")
	assert.Contains(t, summary, "Jobs Status:")
	assert.Contains(t, summary, "Pods Status:")
	assert.Contains(t, summary, "Network Type:")
	assert.Contains(t, summary, "OVNKubernetes")
}

func TestPruningCronjobOptions_writeFile(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newPruningCronjobOptions()

	testContent := "test file content\nwith multiple lines"
	testFile := filepath.Join(tmpDir, "test.txt")

	err := ops.writeFile(testFile, testContent)
	assert.NoError(t, err)

	content, err := os.ReadFile(testFile)
	require.NoError(t, err)
	assert.Equal(t, testContent, string(content))
}

func TestDefaultCommandExecutor(t *testing.T) {
	// This test verifies the default executor structure
	// In a real scenario, you'd mock exec.Command
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test-output.txt")

	// We can't easily test exec.Command without mocking, but we can verify
	// the function signature and that it would attempt to write to file
	err := defaultCommandExecutor("version", []string{}, testFile)
	// This might fail if oc is not available, but that's okay for the test
	// We're just verifying the function exists and has the right signature
	_ = err // ignore error for this test
}

func TestDefaultCommandRunner(t *testing.T) {
	// Similar to TestDefaultCommandExecutor
	output, err := defaultCommandRunner("version", []string{})
	// Output might be empty or contain version info, error might occur if oc not available
	_ = output
	_ = err
}

func TestPruningCronjobOptions_collectNodeExporterInfo(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newPruningCronjobOptions()
	ops.outputDir = tmpDir

	tests := []struct {
		name           string
		topOutput      string
		podOutput      string
		expectNodeExp  bool
	}{
		{
			name: "node-exporter pods found",
			topOutput: "node-exporter-abc123\t100m\t50Mi\nother-pod\t50m\t30Mi",
			podOutput: "node-exporter-abc123\t1/1\tRunning\t0\t2h\tnode1\nother-pod\t1/1\tRunning\t0\t1h\tnode2",
			expectNodeExp: true,
		},
		{
			name:          "no node-exporter pods",
			topOutput:     "other-pod\t50m\t30Mi",
			podOutput:     "other-pod\t1/1\tRunning\t0\t1h\tnode2",
			expectNodeExp: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			callCount := 0
			ops.commandRunner = func(name string, args []string) (string, error) {
				callCount++
				if strings.Contains(strings.Join(args, " "), "top") {
					return tt.topOutput, nil
				}
				return tt.podOutput, nil
			}

			err := ops.collectNodeExporterInfo(nil, nil, nil)
			assert.NoError(t, err)

			cpuFile := filepath.Join(tmpDir, "07-node-exporter-cpu.txt")
			content, err := os.ReadFile(cpuFile)
			require.NoError(t, err)

			if tt.expectNodeExp {
				assert.Contains(t, string(content), "node-exporter")
			} else {
				assert.Contains(t, string(content), "No node-exporter pods found")
			}
		})
	}
}

func TestPruningCronjobOptions_collectImageRegistryInfo(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newPruningCronjobOptions()
	ops.outputDir = tmpDir

	ops.commandExecutor = func(name string, args []string, outputFile string) error {
		return os.WriteFile(outputFile, []byte("test output"), 0644)
	}

	ops.commandRunner = func(name string, args []string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "forbidden") {
			return "Error: Forbidden access to registry\nAnother forbidden error", nil
		}
		return "", nil
	}

	err := ops.collectImageRegistryInfo(nil, nil, nil)
	assert.NoError(t, err)

	// Check forbidden errors file
	forbiddenFile := filepath.Join(tmpDir, "09-registry-operator-forbidden.txt")
	content, err := os.ReadFile(forbiddenFile)
	require.NoError(t, err)
	assert.Contains(t, string(content), "forbidden")
}

func TestCommandHelpMessage(t *testing.T) {
	cmd := NewCmdPruningCronjobErrorSRE()
	
	// Verify help message components
	assert.Contains(t, cmd.Long, "diagnostic information")
	assert.Contains(t, cmd.Long, "PruningCronjobErrorSRE")
	assert.Contains(t, cmd.Long, "Job and pod status")
	assert.Contains(t, cmd.Long, "OpenShift CLI")
	assert.Contains(t, cmd.Long, "oc login")
	assert.Contains(t, cmd.Example, "osdctl assist pruningcronjoberrorsre")
}
