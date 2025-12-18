package assist

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCmdClusterProvisioningFailure(t *testing.T) {
	cmd := NewCmdClusterProvisioningFailure()

	assert.NotNil(t, cmd)
	assert.Equal(t, "cluster-provisioning-failure", cmd.Use)
	assert.Contains(t, cmd.Short, "Collect diagnostic information")
	assert.Contains(t, cmd.Long, "ClusterProvisioningFailure")
	assert.Contains(t, cmd.Long, "diagnostic data")
	assert.NotEmpty(t, cmd.Example)
}

func TestClusterProvisioningFailureOptions_complete(t *testing.T) {
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
			ops := newClusterProvisioningFailureOptions()
			ops.outputDir = tt.outputDir

			cmd := NewCmdClusterProvisioningFailure()
			err := ops.complete(cmd, []string{})

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.checkDir {
					assert.Contains(t, ops.outputDir, "cluster-provisioning-failure-diagnostics-")
				} else {
					assert.Equal(t, tt.outputDir, ops.outputDir)
				}
			}
		})
	}
}

func TestClusterProvisioningFailureOptions_getClusterID(t *testing.T) {
	tests := []struct {
		name        string
		mockOutput  string
		mockError   error
		expectedID  string
		expectedErr bool
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
			expectedErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ops := newClusterProvisioningFailureOptions()
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

func TestClusterProvisioningFailureOptions_collectClusterVersion(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newClusterProvisioningFailureOptions()
	ops.outputDir = tmpDir

	commandCalls := 0
	ops.commandExecutor = func(name string, args []string, outputFile string) error {
		commandCalls++
		return os.WriteFile(outputFile, []byte("test output"), 0644)
	}

	err := ops.collectClusterVersion(nil, nil, nil)
	assert.NoError(t, err)

	// Verify that commands were called
	assert.Equal(t, 2, commandCalls)

	// Verify files were created
	txtFile := filepath.Join(tmpDir, "01-clusterversion.txt")
	yamlFile := filepath.Join(tmpDir, "01-clusterversion.yaml")

	_, err = os.Stat(txtFile)
	assert.NoError(t, err)

	_, err = os.Stat(yamlFile)
	assert.NoError(t, err)
}

func TestClusterProvisioningFailureOptions_collectClusterOperators(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newClusterProvisioningFailureOptions()
	ops.outputDir = tmpDir

	commandCalls := 0
	ops.commandExecutor = func(name string, args []string, outputFile string) error {
		commandCalls++
		return os.WriteFile(outputFile, []byte("test output"), 0644)
	}

	// Mock commandRunner to return JSON with degraded operators
	ops.commandRunner = func(name string, args []string) (string, error) {
		return `{
			"items": [
				{
					"metadata": {"name": "kube-apiserver"},
					"status": {
						"conditions": [
							{"type": "Degraded", "status": "True"}
						]
					}
				},
				{
					"metadata": {"name": "etcd"},
					"status": {
						"conditions": [
							{"type": "Available", "status": "False"}
						]
					}
				}
			]
		}`, nil
	}

	err := ops.collectClusterOperators(nil, nil, nil)
	assert.NoError(t, err)

	// Verify base files were created
	txtFile := filepath.Join(tmpDir, "02-clusteroperators.txt")
	yamlFile := filepath.Join(tmpDir, "02-clusteroperators.yaml")

	_, err = os.Stat(txtFile)
	assert.NoError(t, err)

	_, err = os.Stat(yamlFile)
	assert.NoError(t, err)

	// Verify describe files for degraded operators were created
	apiServerDescribe := filepath.Join(tmpDir, "03-clusteroperator-describe-kube-apiserver.txt")
	etcdDescribe := filepath.Join(tmpDir, "03-clusteroperator-describe-etcd.txt")

	_, err = os.Stat(apiServerDescribe)
	assert.NoError(t, err)

	_, err = os.Stat(etcdDescribe)
	assert.NoError(t, err)
}

func TestClusterProvisioningFailureOptions_collectMachines(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newClusterProvisioningFailureOptions()
	ops.outputDir = tmpDir

	commandCalls := 0
	ops.commandExecutor = func(name string, args []string, outputFile string) error {
		commandCalls++
		return os.WriteFile(outputFile, []byte("test output"), 0644)
	}

	err := ops.collectMachines(nil, nil, nil)
	assert.NoError(t, err)

	// Verify that commands were called (4 commands: machinesets txt+yaml, machines txt+yaml)
	assert.Equal(t, 4, commandCalls)

	// Verify files were created
	machinesetsFile := filepath.Join(tmpDir, "04-machinesets.txt")
	machinesFile := filepath.Join(tmpDir, "05-machines.txt")

	_, err = os.Stat(machinesetsFile)
	assert.NoError(t, err)

	_, err = os.Stat(machinesFile)
	assert.NoError(t, err)
}

func TestClusterProvisioningFailureOptions_collectNodes(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newClusterProvisioningFailureOptions()
	ops.outputDir = tmpDir

	commandCalls := 0
	ops.commandExecutor = func(name string, args []string, outputFile string) error {
		commandCalls++
		return os.WriteFile(outputFile, []byte("test output"), 0644)
	}

	// Mock commandRunner to return JSON with not-ready nodes
	ops.commandRunner = func(name string, args []string) (string, error) {
		return `{
			"items": [
				{
					"metadata": {"name": "node1"},
					"status": {
						"conditions": [
							{"type": "Ready", "status": "True"}
						]
					}
				},
				{
					"metadata": {"name": "node2"},
					"status": {
						"conditions": [
							{"type": "Ready", "status": "False"}
						]
					}
				}
			]
		}`, nil
	}

	err := ops.collectNodes(nil, nil, nil)
	assert.NoError(t, err)

	// Verify base files were created
	nodesFile := filepath.Join(tmpDir, "06-nodes.txt")
	_, err = os.Stat(nodesFile)
	assert.NoError(t, err)

	// Verify describe file for not-ready node was created
	nodeDescribe := filepath.Join(tmpDir, "07-node-describe-node2.txt")
	_, err = os.Stat(nodeDescribe)
	assert.NoError(t, err)
}

func TestClusterProvisioningFailureOptions_collectInfrastructure(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newClusterProvisioningFailureOptions()
	ops.outputDir = tmpDir

	commandCalls := 0
	ops.commandExecutor = func(name string, args []string, outputFile string) error {
		commandCalls++
		return os.WriteFile(outputFile, []byte("test output"), 0644)
	}

	err := ops.collectInfrastructure(nil, nil, nil)
	assert.NoError(t, err)

	// Verify that commands were called (3 commands: infrastructure, dns, network)
	assert.Equal(t, 3, commandCalls)

	// Verify files were created
	infraFile := filepath.Join(tmpDir, "08-infrastructure.yaml")
	dnsFile := filepath.Join(tmpDir, "08-dns.yaml")
	networkFile := filepath.Join(tmpDir, "08-network.yaml")

	_, err = os.Stat(infraFile)
	assert.NoError(t, err)

	_, err = os.Stat(dnsFile)
	assert.NoError(t, err)

	_, err = os.Stat(networkFile)
	assert.NoError(t, err)
}

func TestClusterProvisioningFailureOptions_collectInstallConfig(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newClusterProvisioningFailureOptions()
	ops.outputDir = tmpDir

	ops.commandExecutor = func(name string, args []string, outputFile string) error {
		return os.WriteFile(outputFile, []byte("test output"), 0644)
	}

	err := ops.collectInstallConfig(nil, nil, nil)
	assert.NoError(t, err)

	// Verify file was created
	installConfigFile := filepath.Join(tmpDir, "09-install-config.yaml")
	_, err = os.Stat(installConfigFile)
	assert.NoError(t, err)
}

func TestClusterProvisioningFailureOptions_collectEvents(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newClusterProvisioningFailureOptions()
	ops.outputDir = tmpDir

	commandCalls := 0
	ops.commandExecutor = func(name string, args []string, outputFile string) error {
		commandCalls++
		return os.WriteFile(outputFile, []byte("test output"), 0644)
	}

	err := ops.collectEvents(nil, nil, nil)
	assert.NoError(t, err)

	// Verify files were created for each namespace + all events
	eventsFile := filepath.Join(tmpDir, "10-events-all.txt")
	_, err = os.Stat(eventsFile)
	assert.NoError(t, err)

	// Check for specific namespace event files
	machineAPIEventsFile := filepath.Join(tmpDir, "10-events-openshift-machine-api.txt")
	_, err = os.Stat(machineAPIEventsFile)
	assert.NoError(t, err)
}

func TestClusterProvisioningFailureOptions_collectOperatorLogs(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newClusterProvisioningFailureOptions()
	ops.outputDir = tmpDir

	commandCalls := 0
	ops.commandExecutor = func(name string, args []string, outputFile string) error {
		commandCalls++
		return os.WriteFile(outputFile, []byte("test output"), 0644)
	}

	err := ops.collectOperatorLogs(nil, nil, nil)
	assert.NoError(t, err)

	// Verify that log files were created
	cvoLogsFile := filepath.Join(tmpDir, "11-logs-cluster-version-operator.txt")
	_, err = os.Stat(cvoLogsFile)
	assert.NoError(t, err)

	machineAPILogsFile := filepath.Join(tmpDir, "11-logs-machine-api-operator.txt")
	_, err = os.Stat(machineAPILogsFile)
	assert.NoError(t, err)
}

func TestClusterProvisioningFailureOptions_collectPodsInCriticalNamespaces(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newClusterProvisioningFailureOptions()
	ops.outputDir = tmpDir

	commandCalls := 0
	ops.commandExecutor = func(name string, args []string, outputFile string) error {
		commandCalls++
		return os.WriteFile(outputFile, []byte("test output"), 0644)
	}

	err := ops.collectPodsInCriticalNamespaces(nil, nil, nil)
	assert.NoError(t, err)

	// Verify files were created for critical namespaces
	cvoPodsFile := filepath.Join(tmpDir, "12-pods-openshift-cluster-version.txt")
	_, err = os.Stat(cvoPodsFile)
	assert.NoError(t, err)

	machineAPIPodsFile := filepath.Join(tmpDir, "12-pods-openshift-machine-api.txt")
	_, err = os.Stat(machineAPIPodsFile)
	assert.NoError(t, err)
}

func TestClusterProvisioningFailureOptions_generateSummary(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newClusterProvisioningFailureOptions()
	ops.outputDir = tmpDir

	// Create some test files to be included in summary
	testFiles := map[string]string{
		"01-clusterversion.txt":   "VERSION v4.12.0",
		"02-clusteroperators.txt": "kube-apiserver Available",
		"06-nodes.txt":            "node1 Ready",
		"05-machines.txt":         "machine-1 Running",
	}

	for filename, content := range testFiles {
		err := os.WriteFile(filepath.Join(tmpDir, filename), []byte(content), 0644)
		require.NoError(t, err)
	}

	err := ops.generateSummary("test-cluster-id", nil)
	assert.NoError(t, err)

	// Verify summary file was created
	summaryFile := filepath.Join(tmpDir, "00-SUMMARY.txt")
	_, err = os.Stat(summaryFile)
	assert.NoError(t, err)

	// Read and verify summary content
	content, err := os.ReadFile(summaryFile)
	require.NoError(t, err)

	summaryContent := string(content)
	assert.Contains(t, summaryContent, "ClusterProvisioningFailure Diagnostic Collection Summary")
	assert.Contains(t, summaryContent, "test-cluster-id")
	assert.Contains(t, summaryContent, "Files Collected:")
	assert.Contains(t, summaryContent, "Key Information:")
}

func TestClusterProvisioningFailureOptions_writeFile(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newClusterProvisioningFailureOptions()

	testFile := filepath.Join(tmpDir, "test-file.txt")
	testContent := "test content"

	err := ops.writeFile(testFile, testContent)
	assert.NoError(t, err)

	// Verify file was created with correct content
	content, err := os.ReadFile(testFile)
	require.NoError(t, err)
	assert.Equal(t, testContent, string(content))
}

func TestClusterProvisioningFailureOptions_extractAndReadDiagnostics(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newClusterProvisioningFailureOptions()
	ops.outputDir = tmpDir

	// Create test diagnostic files
	testFiles := map[string]string{
		"00-SUMMARY.txt":          "Summary content",
		"01-clusterversion.txt":   "ClusterVersion content",
		"02-clusteroperators.txt": "ClusterOperators content",
	}

	for filename, content := range testFiles {
		err := os.WriteFile(filepath.Join(tmpDir, filename), []byte(content), 0644)
		require.NoError(t, err)
	}

	content, err := ops.extractAndReadDiagnostics()
	assert.NoError(t, err)
	assert.NotEmpty(t, content)

	// Verify content includes test files
	assert.Contains(t, content, "=== 00-SUMMARY.txt ===")
	assert.Contains(t, content, "Summary content")
	assert.Contains(t, content, "=== 01-clusterversion.txt ===")
	assert.Contains(t, content, "ClusterVersion content")
}

func TestClusterProvisioningFailureOptions_appendToFile(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newClusterProvisioningFailureOptions()

	testFile := filepath.Join(tmpDir, "test-append.txt")

	// Write initial content
	err := os.WriteFile(testFile, []byte("initial content\n"), 0644)
	require.NoError(t, err)

	// Append new content
	err = ops.appendToFile(testFile, "appended content\n")
	assert.NoError(t, err)

	// Verify both contents are present
	content, err := os.ReadFile(testFile)
	require.NoError(t, err)
	assert.Contains(t, string(content), "initial content")
	assert.Contains(t, string(content), "appended content")
}

func TestClusterProvisioningFailureOptions_createInstallLogsNote(t *testing.T) {
	tests := []struct {
		name              string
		clusterInternalID string
		expectedInNote    []string
	}{
		{
			name:              "with cluster ID",
			clusterInternalID: "test-cluster-123",
			expectedInNote: []string{
				"Cluster Internal ID: test-cluster-123",
				"ocm get /api/clusters_mgmt/v1/clusters/test-cluster-123/resources",
				"ocm get cluster test-cluster-123",
			},
		},
		{
			name:              "without cluster ID",
			clusterInternalID: "",
			expectedInNote: []string{
				"${INTERNAL_ID}",
				"Find your cluster's INTERNAL_ID",
				"ocm list clusters",
				"TIP: You can re-run this command with --cluster-id flag",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			ops := newClusterProvisioningFailureOptions()
			ops.outputDir = tmpDir
			ops.clusterInternalID = tt.clusterInternalID

			err := ops.createInstallLogsNote(nil, nil)
			assert.NoError(t, err)

			// Verify note file was created
			noteFile := filepath.Join(tmpDir, "00-INSTALL-LOGS-COLLECTION.txt")
			_, err = os.Stat(noteFile)
			assert.NoError(t, err)

			// Read and verify content
			content, err := os.ReadFile(noteFile)
			require.NoError(t, err)

			noteContent := string(content)
			for _, expected := range tt.expectedInNote {
				assert.Contains(t, noteContent, expected, "Note should contain: %s", expected)
			}
		})
	}
}
