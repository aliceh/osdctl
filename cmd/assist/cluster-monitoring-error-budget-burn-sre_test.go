package assist

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCmdClusterMonitoringErrorBudgetBurnSRE(t *testing.T) {
	cmd := NewCmdClusterMonitoringErrorBudgetBurnSRE()
	
	assert.NotNil(t, cmd)
	assert.Equal(t, "cluster-monitoring-error-budget-burn-sre", cmd.Use)
	assert.Contains(t, cmd.Short, "Collect diagnostic information")
	assert.Contains(t, cmd.Long, "ClusterMonitoringErrorBudgetBurnSRE")
	assert.Contains(t, cmd.Long, "diagnostic data")
	assert.NotEmpty(t, cmd.Example)
}

func TestClusterMonitoringErrorBudgetBurnOptions_complete(t *testing.T) {
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
			ops := newClusterMonitoringErrorBudgetBurnOptions()
			ops.outputDir = tt.outputDir
			
			cmd := NewCmdClusterMonitoringErrorBudgetBurnSRE()
			err := ops.complete(cmd, []string{})
			
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.checkDir {
					assert.Contains(t, ops.outputDir, "cluster-monitoring-error-budget-burn-diagnostics-")
				} else {
					assert.Equal(t, tt.outputDir, ops.outputDir)
				}
			}
		})
	}
}

func TestClusterMonitoringErrorBudgetBurnOptions_getClusterID(t *testing.T) {
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
			ops := newClusterMonitoringErrorBudgetBurnOptions()
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

func TestClusterMonitoringErrorBudgetBurnOptions_collectMonitoringOperator(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newClusterMonitoringErrorBudgetBurnOptions()
	ops.outputDir = tmpDir

	commandCalls := 0
	ops.commandExecutor = func(name string, args []string, outputFile string) error {
		commandCalls++
		// Simulate successful command execution
		return os.WriteFile(outputFile, []byte("test output"), 0644)
	}

	err := ops.collectMonitoringOperator(nil, nil, nil)
	assert.NoError(t, err)

	// Verify that commands were called
	assert.Equal(t, 2, commandCalls)

	// Verify files were created
	yamlFile := filepath.Join(tmpDir, "01-monitoring-clusteroperator.yaml")
	txtFile := filepath.Join(tmpDir, "01-monitoring-clusteroperator.txt")
	
	_, err = os.Stat(yamlFile)
	assert.NoError(t, err)
	
	_, err = os.Stat(txtFile)
	assert.NoError(t, err)
}

func TestClusterMonitoringErrorBudgetBurnOptions_collectMonitoringPods(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newClusterMonitoringErrorBudgetBurnOptions()
	ops.outputDir = tmpDir

	commandCalls := 0
	ops.commandExecutor = func(name string, args []string, outputFile string) error {
		commandCalls++
		// Simulate successful command execution
		return os.WriteFile(outputFile, []byte("test output"), 0644)
	}

	err := ops.collectMonitoringPods(nil, nil, nil)
	assert.NoError(t, err)

	// Verify that commands were called
	assert.Equal(t, 2, commandCalls)

	// Verify files were created
	txtFile := filepath.Join(tmpDir, "02-monitoring-pods.txt")
	yamlFile := filepath.Join(tmpDir, "02-monitoring-pods.yaml")
	
	_, err = os.Stat(txtFile)
	assert.NoError(t, err)
	
	_, err = os.Stat(yamlFile)
	assert.NoError(t, err)
}

func TestClusterMonitoringErrorBudgetBurnOptions_collectPrometheusCRDs(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newClusterMonitoringErrorBudgetBurnOptions()
	ops.outputDir = tmpDir

	commandCalls := 0
	ops.commandExecutor = func(name string, args []string, outputFile string) error {
		commandCalls++
		// Simulate successful command execution
		return os.WriteFile(outputFile, []byte("test output"), 0644)
	}

	err := ops.collectPrometheusCRDs(nil, nil, nil)
	assert.NoError(t, err)

	// Verify that commands were called
	assert.Equal(t, 2, commandCalls)

	// Verify files were created
	txtFile := filepath.Join(tmpDir, "04-prometheus-crds.txt")
	yamlFile := filepath.Join(tmpDir, "04-prometheus-crds.yaml")
	
	_, err = os.Stat(txtFile)
	assert.NoError(t, err)
	
	_, err = os.Stat(yamlFile)
	assert.NoError(t, err)
}

func TestClusterMonitoringErrorBudgetBurnOptions_generateSummary(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newClusterMonitoringErrorBudgetBurnOptions()
	ops.outputDir = tmpDir

	// Create some test files
	testFiles := []string{
		"01-monitoring-clusteroperator.txt",
		"02-monitoring-pods.txt",
		"03-monitoring-events.txt",
		"04-prometheus-crds.txt",
		"05-cmo-logs.txt",
		"06-resource-quotas.txt",
		"07-cluster-version.yaml",
	}

	for _, filename := range testFiles {
		err := os.WriteFile(filepath.Join(tmpDir, filename), []byte("test content"), 0644)
		require.NoError(t, err)
	}

	// Write operator and prometheus content
	err := os.WriteFile(filepath.Join(tmpDir, "01-monitoring-clusteroperator.txt"), []byte("NAME\tSTATUS\nmonitoring\tAvailable"), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tmpDir, "04-prometheus-crds.txt"), []byte("NAMESPACE\tNAME\nopenshift-monitoring\tprometheus-k8s"), 0644)
	require.NoError(t, err)

	err = ops.generateSummary("test-cluster-id", nil)
	assert.NoError(t, err)

	// Verify summary file was created
	summaryFile := filepath.Join(tmpDir, "00-SUMMARY.txt")
	content, err := os.ReadFile(summaryFile)
	require.NoError(t, err)

	summary := string(content)
	assert.Contains(t, summary, "ClusterMonitoringErrorBudgetBurnSRE Diagnostic Collection Summary")
	assert.Contains(t, summary, "test-cluster-id")
	assert.Contains(t, summary, "01-monitoring-clusteroperator.txt")
	assert.Contains(t, summary, "02-monitoring-pods.txt")
	assert.Contains(t, summary, "Monitoring Cluster Operator Status:")
	assert.Contains(t, summary, "Prometheus CRDs")
}

func TestClusterMonitoringErrorBudgetBurnOptions_writeFile(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newClusterMonitoringErrorBudgetBurnOptions()

	testContent := "test file content\nwith multiple lines"
	testFile := filepath.Join(tmpDir, "test.txt")

	err := ops.writeFile(testFile, testContent)
	assert.NoError(t, err)

	content, err := os.ReadFile(testFile)
	require.NoError(t, err)
	assert.Equal(t, testContent, string(content))
}

func TestClusterMonitoringErrorBudgetBurnOptions_extractAndReadDiagnostics(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newClusterMonitoringErrorBudgetBurnOptions()
	ops.outputDir = tmpDir

	// Create priority files
	priorityFiles := []string{
		"00-SUMMARY.txt",
		"01-monitoring-clusteroperator.txt",
		"02-monitoring-pods.txt",
		"03-monitoring-events.txt",
		"04-prometheus-crds.txt",
		"05-cmo-logs.txt",
		"06-resource-quotas.txt",
	}

	for _, filename := range priorityFiles {
		content := fmt.Sprintf("Content for %s", filename)
		err := os.WriteFile(filepath.Join(tmpDir, filename), []byte(content), 0644)
		require.NoError(t, err)
	}

	// Create YAML files
	yamlFiles := []string{
		"01-monitoring-clusteroperator.yaml",
		"02-monitoring-pods.yaml",
		"04-prometheus-crds.yaml",
		"07-cluster-version.yaml",
	}

	for _, filename := range yamlFiles {
		content := fmt.Sprintf("yaml content for %s", filename)
		err := os.WriteFile(filepath.Join(tmpDir, filename), []byte(content), 0644)
		require.NoError(t, err)
	}

	diagnostics, err := ops.extractAndReadDiagnostics()
	assert.NoError(t, err)

	// Verify all priority files are included
	for _, filename := range priorityFiles {
		assert.Contains(t, diagnostics, filename)
		assert.Contains(t, diagnostics, fmt.Sprintf("Content for %s", filename))
	}

	// Verify YAML files are included
	for _, filename := range yamlFiles {
		assert.Contains(t, diagnostics, filename)
	}
}

func TestClusterMonitoringErrorBudgetBurnOptions_collectMonitoringEvents(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newClusterMonitoringErrorBudgetBurnOptions()
	ops.outputDir = tmpDir

	ops.commandExecutor = func(name string, args []string, outputFile string) error {
		return os.WriteFile(outputFile, []byte("test events output"), 0644)
	}

	err := ops.collectMonitoringEvents(nil, nil, nil)
	assert.NoError(t, err)

	// Verify file was created
	eventsFile := filepath.Join(tmpDir, "03-monitoring-events.txt")
	content, err := os.ReadFile(eventsFile)
	require.NoError(t, err)
	assert.Contains(t, string(content), "test events output")
}

func TestClusterMonitoringErrorBudgetBurnOptions_collectMonitoringOperatorLogs(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newClusterMonitoringErrorBudgetBurnOptions()
	ops.outputDir = tmpDir

	commandCalls := 0
	ops.commandExecutor = func(name string, args []string, outputFile string) error {
		commandCalls++
		return os.WriteFile(outputFile, []byte("test logs output"), 0644)
	}

	err := ops.collectMonitoringOperatorLogs(nil, nil, nil)
	assert.NoError(t, err)

	// Verify that commands were called
	assert.Equal(t, 2, commandCalls)

	// Verify files were created
	logsFile := filepath.Join(tmpDir, "05-cmo-logs.txt")
	allContainersFile := filepath.Join(tmpDir, "05-cmo-logs-all-containers.txt")
	
	_, err = os.Stat(logsFile)
	assert.NoError(t, err)
	
	_, err = os.Stat(allContainersFile)
	assert.NoError(t, err)
}

func TestClusterMonitoringErrorBudgetBurnOptions_collectResourceQuotas(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newClusterMonitoringErrorBudgetBurnOptions()
	ops.outputDir = tmpDir

	ops.commandExecutor = func(name string, args []string, outputFile string) error {
		return os.WriteFile(outputFile, []byte("test resource quotas"), 0644)
	}

	err := ops.collectResourceQuotas(nil, nil, nil)
	assert.NoError(t, err)

	// Verify file was created
	quotasFile := filepath.Join(tmpDir, "06-resource-quotas.txt")
	content, err := os.ReadFile(quotasFile)
	require.NoError(t, err)
	assert.Contains(t, string(content), "test resource quotas")
}

func TestClusterMonitoringErrorBudgetBurnOptions_collectClusterVersion(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newClusterMonitoringErrorBudgetBurnOptions()
	ops.outputDir = tmpDir

	ops.commandExecutor = func(name string, args []string, outputFile string) error {
		return os.WriteFile(outputFile, []byte("test cluster version"), 0644)
	}

	err := ops.collectClusterVersion(nil, nil, nil)
	assert.NoError(t, err)

	// Verify file was created
	versionFile := filepath.Join(tmpDir, "07-cluster-version.yaml")
	content, err := os.ReadFile(versionFile)
	require.NoError(t, err)
	assert.Contains(t, string(content), "test cluster version")
}

func TestClusterMonitoringCommandHelpMessage(t *testing.T) {
	cmd := NewCmdClusterMonitoringErrorBudgetBurnSRE()
	
	// Verify help message components
	assert.Contains(t, cmd.Long, "diagnostic information")
	assert.Contains(t, cmd.Long, "ClusterMonitoringErrorBudgetBurnSRE")
	assert.Contains(t, cmd.Long, "Monitoring cluster operator")
	assert.Contains(t, cmd.Long, "OpenShift CLI")
	assert.Contains(t, cmd.Long, "ocm backplane login")
	assert.Contains(t, cmd.Example, "osdctl assist cluster-monitoring-error-budget-burn-sre")
}

func TestClusterMonitoringErrorBudgetBurnOptions_completeWithExistingDir(t *testing.T) {
	tmpDir := t.TempDir()
	
	// Create a test file in the directory
	testFile := filepath.Join(tmpDir, "test.txt")
	err := os.WriteFile(testFile, []byte("test content"), 0644)
	require.NoError(t, err)

	ops := newClusterMonitoringErrorBudgetBurnOptions()
	ops.existingDir = tmpDir
	
	cmd := NewCmdClusterMonitoringErrorBudgetBurnSRE()
	err = ops.complete(cmd, []string{})
	
	assert.NoError(t, err)
	assert.Equal(t, tmpDir, ops.outputDir)
	assert.True(t, ops.skipCollection)
	assert.True(t, ops.enableLLMAnalysis)
}

func TestClusterMonitoringErrorBudgetBurnOptions_completeWithInvalidExistingDir(t *testing.T) {
	ops := newClusterMonitoringErrorBudgetBurnOptions()
	ops.existingDir = "/nonexistent/directory/path"
	
	cmd := NewCmdClusterMonitoringErrorBudgetBurnSRE()
	err := ops.complete(cmd, []string{})
	
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
}

func TestClusterMonitoringErrorBudgetBurnOptions_completeWithFileAsExistingDir(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	err := os.WriteFile(testFile, []byte("test"), 0644)
	require.NoError(t, err)

	ops := newClusterMonitoringErrorBudgetBurnOptions()
	ops.existingDir = testFile // Pass a file instead of directory
	
	cmd := NewCmdClusterMonitoringErrorBudgetBurnSRE()
	err = ops.complete(cmd, []string{})
	
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
}

