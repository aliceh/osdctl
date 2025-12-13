package assist

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCmdDynatraceMonitoringStackDownSRE(t *testing.T) {
	cmd := NewCmdDynatraceMonitoringStackDownSRE()
	
	assert.NotNil(t, cmd)
	assert.Equal(t, "dynatrace-monitoring-stack-down-sre", cmd.Use)
	assert.Contains(t, cmd.Short, "Collect diagnostic information")
	assert.Contains(t, cmd.Long, "DynatraceMonitoringStackDownSRE")
	assert.Contains(t, cmd.Long, "diagnostic data")
	assert.NotEmpty(t, cmd.Example)
}

func TestDynatraceMonitoringStackDownOptions_complete(t *testing.T) {
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
			ops := newDynatraceMonitoringStackDownOptions()
			ops.outputDir = tt.outputDir
			
			cmd := NewCmdDynatraceMonitoringStackDownSRE()
			err := ops.complete(cmd, []string{})
			
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.checkDir {
					assert.Contains(t, ops.outputDir, "dynatrace-monitoring-stack-down-diagnostics-")
				} else {
					assert.Equal(t, tt.outputDir, ops.outputDir)
				}
			}
		})
	}
}

func TestDynatraceMonitoringStackDownOptions_getClusterID(t *testing.T) {
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
			expectedErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ops := newDynatraceMonitoringStackDownOptions()
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

func TestDynatraceMonitoringStackDownOptions_collectDeployments(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newDynatraceMonitoringStackDownOptions()
	ops.outputDir = tmpDir

	commandCalls := 0
	ops.commandExecutor = func(name string, args []string, outputFile string) error {
		commandCalls++
		return os.WriteFile(outputFile, []byte("test output"), 0644)
	}

	err := ops.collectDeployments(nil, nil, nil)
	assert.NoError(t, err)

	// Verify that commands were called
	assert.Equal(t, 2, commandCalls)

	// Verify files were created
	txtFile := filepath.Join(tmpDir, "01-deployments.txt")
	yamlFile := filepath.Join(tmpDir, "01-deployments.yaml")
	
	_, err = os.Stat(txtFile)
	assert.NoError(t, err)
	
	_, err = os.Stat(yamlFile)
	assert.NoError(t, err)
}

func TestDynatraceMonitoringStackDownOptions_collectStatefulSets(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newDynatraceMonitoringStackDownOptions()
	ops.outputDir = tmpDir

	commandCalls := 0
	ops.commandExecutor = func(name string, args []string, outputFile string) error {
		commandCalls++
		return os.WriteFile(outputFile, []byte("test output"), 0644)
	}

	err := ops.collectStatefulSets(nil, nil, nil)
	assert.NoError(t, err)

	// Verify that commands were called
	assert.Equal(t, 2, commandCalls)

	// Verify files were created
	txtFile := filepath.Join(tmpDir, "02-statefulsets-activegate.txt")
	yamlFile := filepath.Join(tmpDir, "02-statefulsets-activegate.yaml")
	
	_, err = os.Stat(txtFile)
	assert.NoError(t, err)
	
	_, err = os.Stat(yamlFile)
	assert.NoError(t, err)
}

func TestDynatraceMonitoringStackDownOptions_collectDaemonSets(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newDynatraceMonitoringStackDownOptions()
	ops.outputDir = tmpDir

	commandCalls := 0
	ops.commandExecutor = func(name string, args []string, outputFile string) error {
		commandCalls++
		return os.WriteFile(outputFile, []byte("test output"), 0644)
	}

	err := ops.collectDaemonSets(nil, nil, nil)
	assert.NoError(t, err)

	// Verify that commands were called
	assert.Equal(t, 2, commandCalls)

	// Verify files were created
	txtFile := filepath.Join(tmpDir, "03-daemonsets-oneagent.txt")
	yamlFile := filepath.Join(tmpDir, "03-daemonsets-oneagent.yaml")
	
	_, err = os.Stat(txtFile)
	assert.NoError(t, err)
	
	_, err = os.Stat(yamlFile)
	assert.NoError(t, err)
}

func TestDynatraceMonitoringStackDownOptions_collectPods(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newDynatraceMonitoringStackDownOptions()
	ops.outputDir = tmpDir

	commandCalls := 0
	ops.commandExecutor = func(name string, args []string, outputFile string) error {
		commandCalls++
		return os.WriteFile(outputFile, []byte("test output"), 0644)
	}

	// Mock pod JSON with failing pods
	podJSON := `{
		"items": [
			{
				"metadata": {"name": "pod1"},
				"status": {"phase": "Failed"}
			},
			{
				"metadata": {"name": "pod2"},
				"status": {"phase": "Running"}
			}
		]
	}`

	ops.commandRunner = func(name string, args []string) (string, error) {
		if name == "get" && len(args) > 0 && args[0] == "pod" {
			return podJSON, nil
		}
		return "", nil
	}

	err := ops.collectPods(nil, nil, nil)
	assert.NoError(t, err)

	// Verify that commands were called (pods + yaml + describe for failing pod)
	assert.GreaterOrEqual(t, commandCalls, 2)

	// Verify files were created
	txtFile := filepath.Join(tmpDir, "04-pods.txt")
	yamlFile := filepath.Join(tmpDir, "04-pods.yaml")
	
	_, err = os.Stat(txtFile)
	assert.NoError(t, err)
	
	_, err = os.Stat(yamlFile)
	assert.NoError(t, err)

	// Verify describe file was created for failing pod
	describeFile := filepath.Join(tmpDir, "05-pod-describe-pod1.txt")
	_, err = os.Stat(describeFile)
	assert.NoError(t, err)
}

func TestDynatraceMonitoringStackDownOptions_collectEvents(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newDynatraceMonitoringStackDownOptions()
	ops.outputDir = tmpDir

	ops.commandExecutor = func(name string, args []string, outputFile string) error {
		return os.WriteFile(outputFile, []byte("test events"), 0644)
	}

	err := ops.collectEvents(nil, nil, nil)
	assert.NoError(t, err)

	// Verify file was created
	eventsFile := filepath.Join(tmpDir, "06-events.txt")
	content, err := os.ReadFile(eventsFile)
	require.NoError(t, err)
	assert.Contains(t, string(content), "test events")
}

func TestDynatraceMonitoringStackDownOptions_collectComponentLogs(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newDynatraceMonitoringStackDownOptions()
	ops.outputDir = tmpDir

	commandCalls := 0
	ops.commandExecutor = func(name string, args []string, outputFile string) error {
		commandCalls++
		return os.WriteFile(outputFile, []byte("test logs"), 0644)
	}

	err := ops.collectComponentLogs(nil, nil, nil)
	assert.NoError(t, err)

	// Verify that commands were called for all 5 components
	assert.Equal(t, 5, commandCalls)

	// Verify log files were created for all components
	components := []string{"operator", "webhook", "otel", "activegate", "oneagent"}
	for _, comp := range components {
		logFile := filepath.Join(tmpDir, fmt.Sprintf("07-logs-%s.txt", comp))
		_, err := os.Stat(logFile)
		assert.NoError(t, err, "Log file for %s should exist", comp)
	}
}

func TestDynatraceMonitoringStackDownOptions_collectClusterCreationTimestamp(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newDynatraceMonitoringStackDownOptions()
	ops.outputDir = tmpDir

	err := ops.collectClusterCreationTimestamp(nil, nil, nil)
	assert.NoError(t, err)

	// Verify note file was created
	noteFile := filepath.Join(tmpDir, "00-cluster-creation-note.txt")
	content, err := os.ReadFile(noteFile)
	require.NoError(t, err)
	assert.Contains(t, string(content), "ocm get cluster")
	assert.Contains(t, string(content), "creation timestamp")
}

func TestDynatraceMonitoringStackDownOptions_generateSummary(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newDynatraceMonitoringStackDownOptions()
	ops.outputDir = tmpDir

	// Create some test files
	testFiles := []string{
		"01-deployments.txt",
		"02-statefulsets-activegate.txt",
		"03-daemonsets-oneagent.txt",
		"04-pods.txt",
		"06-events.txt",
	}

	for _, filename := range testFiles {
		err := os.WriteFile(filepath.Join(tmpDir, filename), []byte("test content"), 0644)
		require.NoError(t, err)
	}

	// Write specific content for summary
	err := os.WriteFile(filepath.Join(tmpDir, "01-deployments.txt"), []byte("NAME\tSTATUS\ndynatrace-operator\t1/1"), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(tmpDir, "04-pods.txt"), []byte("NAME\tSTATUS\npod1\tRunning"), 0644)
	require.NoError(t, err)

	err = ops.generateSummary("test-cluster-id", nil)
	assert.NoError(t, err)

	// Verify summary file was created
	summaryFile := filepath.Join(tmpDir, "00-SUMMARY.txt")
	content, err := os.ReadFile(summaryFile)
	require.NoError(t, err)

	summary := string(content)
	assert.Contains(t, summary, "DynatraceMonitoringStackDownSRE Diagnostic Collection Summary")
	assert.Contains(t, summary, "test-cluster-id")
	assert.Contains(t, summary, "01-deployments.txt")
	assert.Contains(t, summary, "Deployments Status:")
	assert.Contains(t, summary, "Pods Status:")
	assert.Contains(t, summary, "ActiveGate StatefulSet Status:")
	assert.Contains(t, summary, "OneAgent DaemonSet Status:")
}

func TestDynatraceMonitoringStackDownOptions_writeFile(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newDynatraceMonitoringStackDownOptions()

	testContent := "test file content\nwith multiple lines"
	testFile := filepath.Join(tmpDir, "test.txt")

	err := ops.writeFile(testFile, testContent)
	assert.NoError(t, err)

	content, err := os.ReadFile(testFile)
	require.NoError(t, err)
	assert.Equal(t, testContent, string(content))
}

func TestDynatraceMonitoringStackDownOptions_extractAndReadDiagnostics(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newDynatraceMonitoringStackDownOptions()
	ops.outputDir = tmpDir

	// Create priority files
	priorityFiles := []string{
		"00-SUMMARY.txt",
		"01-deployments.txt",
		"02-statefulsets-activegate.txt",
		"03-daemonsets-oneagent.txt",
		"04-pods.txt",
		"06-events.txt",
	}

	for _, filename := range priorityFiles {
		content := fmt.Sprintf("Content for %s", filename)
		err := os.WriteFile(filepath.Join(tmpDir, filename), []byte(content), 0644)
		require.NoError(t, err)
	}

	// Create YAML files
	yamlFiles := []string{
		"01-deployments.yaml",
		"02-statefulsets-activegate.yaml",
		"03-daemonsets-oneagent.yaml",
		"04-pods.yaml",
	}

	for _, filename := range yamlFiles {
		content := fmt.Sprintf("yaml content for %s", filename)
		err := os.WriteFile(filepath.Join(tmpDir, filename), []byte(content), 0644)
		require.NoError(t, err)
	}

	// Create pod describe files
	podDescribeFiles := []string{
		"05-pod-describe-pod1.txt",
		"05-pod-describe-pod2.txt",
	}

	for _, filename := range podDescribeFiles {
		content := fmt.Sprintf("describe content for %s", filename)
		err := os.WriteFile(filepath.Join(tmpDir, filename), []byte(content), 0644)
		require.NoError(t, err)
	}

	// Create log files
	logFiles := []string{
		"07-logs-operator.txt",
		"07-logs-webhook.txt",
		"07-logs-otel.txt",
		"07-logs-activegate.txt",
		"07-logs-oneagent.txt",
	}

	for _, filename := range logFiles {
		content := fmt.Sprintf("log content for %s", filename)
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

	// Verify log files are included
	for _, filename := range logFiles {
		assert.Contains(t, diagnostics, filename)
	}
}

func TestDynatraceCommandHelpMessage(t *testing.T) {
	cmd := NewCmdDynatraceMonitoringStackDownSRE()
	
	// Verify help message components
	assert.Contains(t, cmd.Long, "diagnostic information")
	assert.Contains(t, cmd.Long, "DynatraceMonitoringStackDownSRE")
	assert.Contains(t, cmd.Long, "Deployments")
	assert.Contains(t, cmd.Long, "OpenShift CLI")
	assert.Contains(t, cmd.Long, "ocm backplane login")
	assert.Contains(t, cmd.Example, "osdctl assist dynatrace-monitoring-stack-down-sre")
}

func TestDynatraceMonitoringStackDownOptions_completeWithExistingDir(t *testing.T) {
	tmpDir := t.TempDir()
	
	// Create a test file in the directory
	testFile := filepath.Join(tmpDir, "test.txt")
	err := os.WriteFile(testFile, []byte("test content"), 0644)
	require.NoError(t, err)

	ops := newDynatraceMonitoringStackDownOptions()
	ops.existingDir = tmpDir
	
	cmd := NewCmdDynatraceMonitoringStackDownSRE()
	err = ops.complete(cmd, []string{})
	
	assert.NoError(t, err)
	assert.Equal(t, tmpDir, ops.outputDir)
	assert.True(t, ops.skipCollection)
	assert.True(t, ops.enableLLMAnalysis)
}

func TestDynatraceMonitoringStackDownOptions_collectPodsWithFailingPods(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newDynatraceMonitoringStackDownOptions()
	ops.outputDir = tmpDir

	describeCalls := 0
	ops.commandExecutor = func(name string, args []string, outputFile string) error {
		if name == "describe" {
			describeCalls++
		}
		return os.WriteFile(outputFile, []byte("test output"), 0644)
	}

	// Mock pod JSON with multiple failing pods
	podJSON := `{
		"items": [
			{
				"metadata": {"name": "failing-pod-1"},
				"status": {"phase": "Failed"}
			},
			{
				"metadata": {"name": "failing-pod-2"},
				"status": {"phase": "Pending"}
			},
			{
				"metadata": {"name": "running-pod"},
				"status": {"phase": "Running"}
			}
		]
	}`

	ops.commandRunner = func(name string, args []string) (string, error) {
		if name == "get" && len(args) > 0 && args[0] == "pod" {
			return podJSON, nil
		}
		return "", nil
	}

	err := ops.collectPods(nil, nil, nil)
	assert.NoError(t, err)

	// Verify describe was called for both failing pods
	assert.Equal(t, 2, describeCalls)

	// Verify describe files were created
	describeFile1 := filepath.Join(tmpDir, "05-pod-describe-failing-pod-1.txt")
	describeFile2 := filepath.Join(tmpDir, "05-pod-describe-failing-pod-2.txt")
	
	_, err = os.Stat(describeFile1)
	assert.NoError(t, err)
	
	_, err = os.Stat(describeFile2)
	assert.NoError(t, err)
}

func TestDynatraceMonitoringStackDownOptions_collectPodsWithNoFailingPods(t *testing.T) {
	tmpDir := t.TempDir()
	ops := newDynatraceMonitoringStackDownOptions()
	ops.outputDir = tmpDir

	describeCalls := 0
	ops.commandExecutor = func(name string, args []string, outputFile string) error {
		if name == "describe" {
			describeCalls++
		}
		return os.WriteFile(outputFile, []byte("test output"), 0644)
	}

	// Mock pod JSON with only running/succeeded pods
	podJSON := `{
		"items": [
			{
				"metadata": {"name": "running-pod-1"},
				"status": {"phase": "Running"}
			},
			{
				"metadata": {"name": "succeeded-pod"},
				"status": {"phase": "Succeeded"}
			}
		]
	}`

	ops.commandRunner = func(name string, args []string) (string, error) {
		if name == "get" && len(args) > 0 && args[0] == "pod" {
			return podJSON, nil
		}
		return "", nil
	}

	err := ops.collectPods(nil, nil, nil)
	assert.NoError(t, err)

	// Verify describe was NOT called since no pods are failing
	assert.Equal(t, 0, describeCalls)
}

