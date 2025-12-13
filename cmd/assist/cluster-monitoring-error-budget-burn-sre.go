package assist

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
)

//go:embed prompts/cluster_monitoring_error_budget_burn_analysis.md
var clusterMonitoringSystemPromptTemplate string

type clusterMonitoringErrorBudgetBurnOptions struct {
	outputDir      string
	existingDir    string // Directory with existing artifacts to analyze
	skipCollection bool   // Skip collection if analyzing existing directory
	// LLM analysis options
	enableLLMAnalysis bool
	llmAPIKey         string
	llmBaseURL        string
	llmModel          string
	// For testing: allows injection of command executor
	commandExecutor func(name string, args []string, outputFile string) error
	commandRunner   func(name string, args []string) (string, error)
}

// NewCmdClusterMonitoringErrorBudgetBurnSRE implements the cluster-monitoring-error-budget-burn-sre command
func NewCmdClusterMonitoringErrorBudgetBurnSRE() *cobra.Command {
	ops := newClusterMonitoringErrorBudgetBurnOptions()
	cmd := &cobra.Command{
		Use:   "cluster-monitoring-error-budget-burn-sre",
		Short: "Collect diagnostic information for ClusterMonitoringErrorBudgetBurnSRE alert",
		Long: `Collects all diagnostic information needed to troubleshoot the ClusterMonitoringErrorBudgetBurnSRE alert.

This command gathers comprehensive diagnostic data including:
  - Monitoring cluster operator status and conditions
  - Cluster monitoring operator logs
  - Prometheus CRDs across all namespaces (to detect second monitoring stack)
  - Pods and events in openshift-monitoring namespace
  - Cluster operator probe metrics information
  - Resource quotas and constraints
  - Cluster version information

All diagnostic files are saved to a timestamped directory and a summary report
is generated. The output can optionally be archived as a tarball.

The command requires:
  - OpenShift CLI (oc) to be installed and available in PATH
  - Active cluster connection (via 'ocm backplane login')

For troubleshooting steps, refer to:
  ~/ops-sop/v4/alerts/ClusterMonitoringErrorBudgetBurnSRE.md
`,
		Example: `  # Collect diagnostics with default output directory
  osdctl assist cluster-monitoring-error-budget-burn-sre

  # Collect diagnostics to a custom directory
  osdctl assist cluster-monitoring-error-budget-burn-sre --output-dir /tmp/my-diagnostics

  # Collect diagnostics and analyze with LLM
  osdctl assist cluster-monitoring-error-budget-burn-sre --analyze

  # Analyze existing directory of diagnostic artifacts
  osdctl assist cluster-monitoring-error-budget-burn-sre --analyze-existing /path/to/existing-diagnostics`,
		Args:              cobra.NoArgs,
		DisableAutoGenTag: true,
		Run: func(cmd *cobra.Command, args []string) {
			cmdutil.CheckErr(ops.complete(cmd, args))
			cmdutil.CheckErr(ops.run())
		},
	}

	cmd.Flags().StringVar(&ops.outputDir, "output-dir", "", "Output directory for diagnostic files (default: cluster-monitoring-error-budget-burn-diagnostics-TIMESTAMP)")
	cmd.Flags().StringVar(&ops.existingDir, "analyze-existing", "", "Path to existing directory of diagnostic artifacts to analyze with LLM (skips collection)")
	cmd.Flags().BoolVar(&ops.enableLLMAnalysis, "analyze", false, "Enable LLM analysis of collected diagnostic files")
	cmd.Flags().StringVar(&ops.llmAPIKey, "llm-api-key", "", "LLM API key (default: checks ~/.config/osdctl OPENAI_API_KEY, then env vars: LLM_API_KEY, OPENAI_API_KEY, etc.)")
	cmd.Flags().StringVar(&ops.llmBaseURL, "llm-base-url", "", "LLM API base URL (default: checks ~/.config/osdctl OPENAI_BASE_URL, then env vars, or https://api.openai.com/v1)")
	cmd.Flags().StringVar(&ops.llmModel, "llm-model", "gpt-4o-mini", "LLM model to use for analysis (default: checks ~/.config/osdctl AI_MODEL_NAME, then env vars, or gpt-4o-mini)")
	return cmd
}

func newClusterMonitoringErrorBudgetBurnOptions() *clusterMonitoringErrorBudgetBurnOptions {
	return &clusterMonitoringErrorBudgetBurnOptions{
		commandExecutor: defaultCommandExecutor,
		commandRunner:   defaultCommandRunner,
	}
}

func (o *clusterMonitoringErrorBudgetBurnOptions) complete(cmd *cobra.Command, _ []string) error {
	// If analyzing existing directory, set it as outputDir and skip collection
	if o.existingDir != "" {
		// Validate existing directory exists
		if info, err := os.Stat(o.existingDir); err != nil {
			return fmt.Errorf("existing directory does not exist or is not accessible: %w", err)
		} else if !info.IsDir() {
			return fmt.Errorf("path is not a directory: %s", o.existingDir)
		}
		o.outputDir = o.existingDir
		o.skipCollection = true
		o.enableLLMAnalysis = true // Automatically enable analysis when using existing directory
	} else if o.outputDir == "" {
		timestamp := time.Now().Format("20060102-150405")
		o.outputDir = fmt.Sprintf("cluster-monitoring-error-budget-burn-diagnostics-%s", timestamp)
	}

	// Set LLM defaults from config file, then environment, then defaults
	if o.enableLLMAnalysis {
		// API Key: Priority: flag > config file > environment variables > error
		if o.llmAPIKey == "" {
			// Check config file first
			if configKey := loadConfigValue("OPENAI_API_KEY", "OPENAI_API_KEY", ""); configKey != "" {
				o.llmAPIKey = configKey
			} else {
				// Check multiple possible environment variable names
				envVarNames := []string{"LLM_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GOOGLE_API_KEY"}
				for _, envVar := range envVarNames {
					if envKey := os.Getenv(envVar); envKey != "" {
						o.llmAPIKey = strings.TrimSpace(envKey)
						break
					}
				}
			}
		} else {
			// Also trim if provided via flag
			o.llmAPIKey = strings.TrimSpace(o.llmAPIKey)
		}

		// Base URL: Priority: flag > config file > environment variables > default
		if o.llmBaseURL == "" {
			// Check config file first
			if configURL := loadConfigValue("OPENAI_BASE_URL", "OPENAI_BASE_URL", ""); configURL != "" {
				o.llmBaseURL = configURL
			} else {
				// Check multiple possible environment variable names
				envVarNames := []string{"LLM_BASE_URL", "OPENAI_BASE_URL", "ANTHROPIC_BASE_URL", "GOOGLE_BASE_URL"}
				for _, envVar := range envVarNames {
					if envURL := os.Getenv(envVar); envURL != "" {
						o.llmBaseURL = strings.TrimSpace(envURL)
						break
					}
				}
				if o.llmBaseURL == "" {
					o.llmBaseURL = "https://api.openai.com/v1"
				}
			}
		}

		// Model: Priority: flag (if explicitly set) > config file > environment variables > default
		// Only override default if flag wasn't explicitly changed by user
		if !cmd.Flags().Changed("llm-model") {
			// Check config file first
			if configModel := loadConfigValue("AI_MODEL_NAME", "AI_MODEL_NAME", ""); configModel != "" {
				o.llmModel = configModel
			} else {
				envVarNames := []string{"LLM_MODEL", "AI_MODEL_NAME", "OPENAI_MODEL", "ANTHROPIC_MODEL", "GOOGLE_MODEL"}
				for _, envVar := range envVarNames {
					if envModel := os.Getenv(envVar); envModel != "" {
						o.llmModel = strings.TrimSpace(envModel)
						break
					}
				}
			}
		}

		// Validate required configuration
		if o.llmAPIKey == "" {
			// Check if any of the common environment variables exist
			envVarNames := []string{"LLM_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GOOGLE_API_KEY"}
			var foundVars []string
			for _, envVar := range envVarNames {
				if os.Getenv(envVar) != "" {
					foundVars = append(foundVars, envVar)
				}
			}
			if len(foundVars) > 0 {
				return fmt.Errorf("LLM analysis enabled but API key from environment variable(s) %v appears to be empty or invalid", foundVars)
			}
			return fmt.Errorf("LLM analysis enabled but no API key provided. Set --llm-api-key flag or one of these environment variables: LLM_API_KEY, OPENAI_API_KEY, ANTHROPIC_API_KEY, GOOGLE_API_KEY")
		}

		// Validate API key format (basic check)
		if err := validateAPIKey(o.llmAPIKey); err != nil {
			// Show first few characters for debugging (safely)
			keyPreview := o.llmAPIKey
			if len(keyPreview) > 20 {
				keyPreview = keyPreview[:20] + "..."
			}
			return fmt.Errorf("LLM API key validation failed: %w\nKey preview (first 20 chars): %s\nPlease verify your API key environment variable (LLM_API_KEY, OPENAI_API_KEY, etc.)", err, keyPreview)
		}
	}

	return nil
}

func (o *clusterMonitoringErrorBudgetBurnOptions) run() error {
	green := color.New(color.FgGreen)
	yellow := color.New(color.FgYellow)
	red := color.New(color.FgRed)

	// Skip collection if analyzing existing directory
	if o.skipCollection {
		green.Println("Analyzing existing diagnostic artifacts...")
		fmt.Printf("Directory: %s\n\n", o.outputDir)

		// Verify directory has some diagnostic files
		files, err := filepath.Glob(filepath.Join(o.outputDir, "*.txt"))
		if err == nil && len(files) == 0 {
			// Also check for yaml/json files
			yamlFiles, _ := filepath.Glob(filepath.Join(o.outputDir, "*.yaml"))
			jsonFiles, _ := filepath.Glob(filepath.Join(o.outputDir, "*.json"))
			if len(yamlFiles) == 0 && len(jsonFiles) == 0 {
				return fmt.Errorf("directory appears to be empty or contains no diagnostic files: %s", o.outputDir)
			}
		}
	} else {
		green.Println("Collecting diagnostic information for ClusterMonitoringErrorBudgetBurnSRE alert...")
		fmt.Printf("Output directory: %s\n\n", o.outputDir)

		// Create output directory
		if err := os.MkdirAll(o.outputDir, 0755); err != nil {
			return fmt.Errorf("failed to create output directory: %w", err)
		}

		// Check if oc is available
		if _, err := exec.LookPath("oc"); err != nil {
			red.Println("Error: 'oc' command not found. Please ensure OpenShift CLI is installed and configured.")
			return err
		}

		// Check if we're logged into a cluster
		if err := o.commandExecutor("cluster-info", []string{}, "/dev/null"); err != nil {
			red.Println("Error: Not logged into a cluster. Please run 'ocm backplane login' first.")
			return err
		}

		// Get cluster info
		green.Println("Cluster Information:")
		clusterID, _ := o.getClusterID()
		fmt.Printf("Cluster ID: %s\n\n", clusterID)

		// Save cluster info
		clusterInfoFile := filepath.Join(o.outputDir, "cluster-info.txt")
		if err := o.writeFile(clusterInfoFile, fmt.Sprintf("Cluster ID: %s\nCollection Date: %s\n", clusterID, time.Now().Format(time.RFC3339))); err != nil {
			return err
		}
		o.commandExecutor("cluster-info", []string{}, clusterInfoFile)

		// Collect all diagnostic information
		if err := o.collectMonitoringOperator(yellow, green, red); err != nil {
			return err
		}
		if err := o.collectMonitoringPods(yellow, green, red); err != nil {
			return err
		}
		if err := o.collectMonitoringEvents(yellow, green, red); err != nil {
			return err
		}
		if err := o.collectPrometheusCRDs(yellow, green, red); err != nil {
			return err
		}
		if err := o.collectMonitoringOperatorLogs(yellow, green, red); err != nil {
			return err
		}
		if err := o.collectResourceQuotas(yellow, green, red); err != nil {
			return err
		}
		if err := o.collectClusterVersion(yellow, green, red); err != nil {
			return err
		}

		// Generate summary
		if err := o.generateSummary(clusterID, green); err != nil {
			return err
		}

		// Create tarball
		green.Println("Creating archive...")
		if err := o.createTarball(); err != nil {
			// Non-fatal error
			fmt.Printf("Warning: Failed to create archive: %v\n", err)
		}
	}

	// LLM Analysis if enabled
	if o.enableLLMAnalysis {
		yellow.Println("\nAnalyzing diagnostics with LLM...")

		// Debug: Show which base URL and model are being used (without exposing API key)
		fmt.Printf("Using LLM endpoint: %s\n", o.llmBaseURL)
		fmt.Printf("Using model: %s\n", o.llmModel)
		if len(o.llmAPIKey) > 0 {
			keyPreview := o.llmAPIKey[:min(10, len(o.llmAPIKey))] + "..." + o.llmAPIKey[max(0, len(o.llmAPIKey)-10):]
			fmt.Printf("API key preview: %s (length: %d)\n", keyPreview, len(o.llmAPIKey))
		}
		fmt.Println()

		diagnosticContent, err := o.extractAndReadDiagnostics()
		if err != nil {
			fmt.Printf("Warning: Failed to extract diagnostic content: %v\n", err)
		} else {
			analysis, err := o.analyzeWithLLM(diagnosticContent)
			if err != nil {
				fmt.Printf("Warning: LLM analysis failed: %v\n", err)
			} else {
				green.Println("\n=== LLM Analysis Results ===")
				fmt.Println(analysis)
				fmt.Println()

				// Save analysis to file
				analysisFile := filepath.Join(o.outputDir, "08-llm-analysis.txt")
				if err := o.writeFile(analysisFile, analysis); err != nil {
					fmt.Printf("Warning: Failed to save LLM analysis: %v\n", err)
				} else {
					green.Printf("✓ LLM analysis saved to 08-llm-analysis.txt\n")
				}
			}
		}
	}

	green.Println("\n========================================")
	green.Println("Collection Complete!")
	green.Println("========================================")
	fmt.Printf("Diagnostic information saved to: %s/\n", o.outputDir)
	if _, err := os.Stat(o.outputDir + ".tar.gz"); err == nil {
		fmt.Printf("Archive created: %s.tar.gz\n", o.outputDir)
	}
	fmt.Println("\nNext steps:")
	fmt.Printf("1. Review the files in %s/\n", o.outputDir)
	fmt.Println("2. Start with 00-SUMMARY.txt for an overview")
	if o.enableLLMAnalysis {
		fmt.Println("3. Review 08-llm-analysis.txt for AI-powered insights")
		fmt.Println("4. Check cluster operator status and logs for error details")
		fmt.Println("5. Refer to ~/ops-sop/v4/alerts/ClusterMonitoringErrorBudgetBurnSRE.md for troubleshooting steps")
	} else {
		fmt.Println("3. Check cluster operator status and logs for error details")
		fmt.Println("4. Refer to ~/ops-sop/v4/alerts/ClusterMonitoringErrorBudgetBurnSRE.md for troubleshooting steps")
		fmt.Println("5. Use --analyze flag to enable LLM analysis")
	}
	fmt.Println()

	return nil
}

func (o *clusterMonitoringErrorBudgetBurnOptions) getClusterID() (string, error) {
	output, err := o.commandRunner("get", []string{"clusterversion", "version", "-o", "jsonpath={.spec.clusterID}"})
	if err != nil || strings.TrimSpace(output) == "" {
		return "N/A", nil
	}
	return strings.TrimSpace(output), nil
}

func (o *clusterMonitoringErrorBudgetBurnOptions) collectMonitoringOperator(yellow, green, red *color.Color) error {
	safeColorPrintln(yellow, "Collecting: Monitoring cluster operator status")
	if err := o.runOCCommand("get", []string{"clusteroperator", "monitoring", "-o", "yaml"}, filepath.Join(o.outputDir, "01-monitoring-clusteroperator.yaml")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect monitoring cluster operator\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 01-monitoring-clusteroperator.yaml\n\n")
	}

	// Also get wide format for quick view
	safeColorPrintln(yellow, "Collecting: Monitoring cluster operator status (wide)")
	if err := o.runOCCommand("get", []string{"clusteroperator", "monitoring", "-o", "wide"}, filepath.Join(o.outputDir, "01-monitoring-clusteroperator.txt")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect monitoring cluster operator (wide)\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 01-monitoring-clusteroperator.txt\n\n")
	}
	return nil
}

func (o *clusterMonitoringErrorBudgetBurnOptions) collectMonitoringPods(yellow, green, red *color.Color) error {
	safeColorPrintln(yellow, "Collecting: Pods in openshift-monitoring namespace")
	if err := o.runOCCommand("get", []string{"pod", "-n", "openshift-monitoring", "-o", "wide"}, filepath.Join(o.outputDir, "02-monitoring-pods.txt")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect monitoring pods\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 02-monitoring-pods.txt\n\n")
	}

	safeColorPrintln(yellow, "Collecting: Pods in openshift-monitoring namespace (yaml)")
	if err := o.runOCCommand("get", []string{"pod", "-n", "openshift-monitoring", "-o", "yaml"}, filepath.Join(o.outputDir, "02-monitoring-pods.yaml")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect monitoring pods yaml\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 02-monitoring-pods.yaml\n\n")
	}
	return nil
}

func (o *clusterMonitoringErrorBudgetBurnOptions) collectMonitoringEvents(yellow, green, red *color.Color) error {
	safeColorPrintln(yellow, "Collecting: Events in openshift-monitoring namespace")
	if err := o.runOCCommand("get", []string{"events", "-n", "openshift-monitoring", "--sort-by=.lastTimestamp"}, filepath.Join(o.outputDir, "03-monitoring-events.txt")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect monitoring events\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 03-monitoring-events.txt\n\n")
	}
	return nil
}

func (o *clusterMonitoringErrorBudgetBurnOptions) collectPrometheusCRDs(yellow, green, red *color.Color) error {
	safeColorPrintln(yellow, "Collecting: Prometheus CRDs across all namespaces (checking for second monitoring stack)")
	if err := o.runOCCommand("get", []string{"prometheus", "-A", "-o", "wide"}, filepath.Join(o.outputDir, "04-prometheus-crds.txt")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect Prometheus CRDs\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 04-prometheus-crds.txt\n\n")
	}

	safeColorPrintln(yellow, "Collecting: Prometheus CRDs across all namespaces (yaml)")
	if err := o.runOCCommand("get", []string{"prometheus", "-A", "-o", "yaml"}, filepath.Join(o.outputDir, "04-prometheus-crds.yaml")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect Prometheus CRDs yaml\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 04-prometheus-crds.yaml\n\n")
	}
	return nil
}

func (o *clusterMonitoringErrorBudgetBurnOptions) collectMonitoringOperatorLogs(yellow, green, red *color.Color) error {
	safeColorPrintln(yellow, "Collecting: Cluster monitoring operator logs")
	if err := o.runOCCommand("logs", []string{"-n", "openshift-monitoring", "-l", "app=cluster-monitoring-operator", "--tail=100"}, filepath.Join(o.outputDir, "05-cmo-logs.txt")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect CMO logs\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 05-cmo-logs.txt\n\n")
	}

	// Get logs from all containers
	safeColorPrintln(yellow, "Collecting: Cluster monitoring operator logs (all containers)")
	if err := o.runOCCommand("logs", []string{"-n", "openshift-monitoring", "-l", "app=cluster-monitoring-operator", "--all-containers=true", "--tail=100"}, filepath.Join(o.outputDir, "05-cmo-logs-all-containers.txt")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect CMO logs (all containers)\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 05-cmo-logs-all-containers.txt\n\n")
	}
	return nil
}

func (o *clusterMonitoringErrorBudgetBurnOptions) collectResourceQuotas(yellow, green, red *color.Color) error {
	safeColorPrintln(yellow, "Collecting: Resource quotas in openshift-monitoring")
	if err := o.runOCCommand("get", []string{"resourcequota", "-n", "openshift-monitoring"}, filepath.Join(o.outputDir, "06-resource-quotas.txt")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect resource quotas\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 06-resource-quotas.txt\n\n")
	}
	return nil
}

func (o *clusterMonitoringErrorBudgetBurnOptions) collectClusterVersion(yellow, green, red *color.Color) error {
	safeColorPrintln(yellow, "Collecting: Cluster version information")
	if err := o.runOCCommand("get", []string{"clusterversion", "version", "-o", "yaml"}, filepath.Join(o.outputDir, "07-cluster-version.yaml")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect cluster version\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 07-cluster-version.yaml\n\n")
	}
	return nil
}

func (o *clusterMonitoringErrorBudgetBurnOptions) runOCCommand(subcommand string, args []string, outputFile string) error {
	return o.commandExecutor(subcommand, args, outputFile)
}

func (o *clusterMonitoringErrorBudgetBurnOptions) runOCCommandWithOutput(subcommand string, args []string) (string, error) {
	return o.commandRunner(subcommand, args)
}

func (o *clusterMonitoringErrorBudgetBurnOptions) writeFile(filePath string, content string) error {
	return os.WriteFile(filePath, []byte(content), 0644)
}

func (o *clusterMonitoringErrorBudgetBurnOptions) generateSummary(clusterID string, green *color.Color) error {
	safeColorPrintln(green, "Generating summary report...")

	summaryFile := filepath.Join(o.outputDir, "00-SUMMARY.txt")
	summary := fmt.Sprintf(`ClusterMonitoringErrorBudgetBurnSRE Diagnostic Collection Summary
====================================================
Collection Date: %s
Cluster ID: %s

Files Collected:
----------------
`, time.Now().Format(time.RFC3339), clusterID)

	// Find all collected files
	var files []string
	err := filepath.Walk(o.outputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			ext := filepath.Ext(path)
			if ext == ".txt" || ext == ".yaml" || ext == ".json" {
				files = append(files, filepath.Base(path))
			}
		}
		return nil
	})
	if err == nil {
		sort.Strings(files)
		for _, file := range files {
			summary += file + "\n"
		}
	}

	summary += "\nKey Information:\n---------------\n"

	// Add monitoring operator status
	operatorFile := filepath.Join(o.outputDir, "01-monitoring-clusteroperator.txt")
	if content, err := os.ReadFile(operatorFile); err == nil {
		summary += "\nMonitoring Cluster Operator Status:\n"
		summary += string(content) + "\n"
	}

	// Add Prometheus CRDs info
	prometheusFile := filepath.Join(o.outputDir, "04-prometheus-crds.txt")
	if content, err := os.ReadFile(prometheusFile); err == nil {
		summary += "\nPrometheus CRDs (check for second monitoring stack):\n"
		summary += string(content) + "\n"
	}

	return o.writeFile(summaryFile, summary)
}

func (o *clusterMonitoringErrorBudgetBurnOptions) createTarball() error {
	cmd := exec.Command("tar", "-czf", o.outputDir+".tar.gz", o.outputDir)
	return cmd.Run()
}

// extractAndReadDiagnostics extracts key diagnostic files and returns their content
func (o *clusterMonitoringErrorBudgetBurnOptions) extractAndReadDiagnostics() (string, error) {
	var content strings.Builder

	// Priority files to read (in order)
	priorityFiles := []string{
		"00-SUMMARY.txt",
		"01-monitoring-clusteroperator.txt",
		"02-monitoring-pods.txt",
		"03-monitoring-events.txt",
		"04-prometheus-crds.txt",
		"05-cmo-logs.txt",
		"06-resource-quotas.txt",
	}

	// Read priority files first
	for _, filename := range priorityFiles {
		filePath := filepath.Join(o.outputDir, filename)
		if data, err := os.ReadFile(filePath); err == nil {
			content.WriteString(fmt.Sprintf("\n=== %s ===\n", filename))
			content.Write(data)
			content.WriteString("\n")
		}
	}

	// Read YAML files for detailed information
	yamlFiles := []string{
		"01-monitoring-clusteroperator.yaml",
		"02-monitoring-pods.yaml",
		"04-prometheus-crds.yaml",
		"07-cluster-version.yaml",
	}

	for _, filename := range yamlFiles {
		filePath := filepath.Join(o.outputDir, filename)
		if data, err := os.ReadFile(filePath); err == nil {
			content.WriteString(fmt.Sprintf("\n=== %s ===\n", filename))
			// Limit YAML size to avoid token limits
			yamlContent := string(data)
			if len(yamlContent) > 15000 {
				yamlContent = yamlContent[:15000] + "\n... (truncated)"
			}
			content.WriteString(yamlContent)
			content.WriteString("\n")
		}
	}

	return content.String(), nil
}

// analyzeWithLLM sends diagnostic content to LLM for analysis
func (o *clusterMonitoringErrorBudgetBurnOptions) analyzeWithLLM(diagnosticContent string) (string, error) {
	systemPrompt := clusterMonitoringSystemPromptTemplate
	// Fallback to default if embed failed (shouldn't happen, but be safe)
	if systemPrompt == "" {
		systemPrompt = `You are an expert OpenShift/Kubernetes Site Reliability Engineer (SRE) specializing in cluster monitoring and observability. Your task is to analyze diagnostic information and assess the health of the monitoring cluster operator for the ClusterMonitoringErrorBudgetBurnSRE alert.

Analyze the provided diagnostic data and provide:
1. Root cause analysis - What is likely causing the error budget burn?
2. Key findings - What are the most important issues identified?
3. Recommended actions - What steps should be taken to resolve the issues?
4. Priority - Rate the severity (Critical/High/Medium/Low/Healthy)

Be concise but thorough. Focus on actionable insights.`
	}

	requestBody := map[string]interface{}{
		"model": o.llmModel,
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": systemPrompt,
			},
			{
				"role":    "user",
				"content": fmt.Sprintf("Please analyze the following ClusterMonitoringErrorBudgetBurnSRE diagnostic information from an OpenShift cluster:\n\n%s", diagnosticContent),
			},
		},
		"temperature": 0.3,
		"max_tokens":  4000,
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	// Construct URL - ensure base URL doesn't have trailing slash, and endpoint does
	baseURL := strings.TrimSuffix(o.llmBaseURL, "/")
	endpoint := "/chat/completions"
	url := baseURL + endpoint

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers - ensure API key is properly trimmed (remove any whitespace, newlines, etc.)
	apiKey := strings.TrimSpace(o.llmAPIKey)
	// Remove any potential newlines or carriage returns that might have been introduced
	apiKey = strings.ReplaceAll(apiKey, "\n", "")
	apiKey = strings.ReplaceAll(apiKey, "\r", "")

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{
		Timeout: 120 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)

		// Parse error response for better error messages
		var errorResp struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Code    string `json:"code"`
			} `json:"error"`
		}

		errorMsg := string(body)
		if err := json.Unmarshal(body, &errorResp); err == nil && errorResp.Error.Message != "" {
			errorMsg = errorResp.Error.Message

			// Provide generic guidance for authentication errors
			if resp.StatusCode == 401 {
				return "", fmt.Errorf("authentication failed (401): %s\n\nTroubleshooting:\n"+
					"1. Verify your API key is correct\n"+
					"2. Ensure there are no extra spaces or newlines in the key\n"+
					"3. Check your environment variables: LLM_API_KEY, OPENAI_API_KEY, etc.\n"+
					"4. Verify the API key format matches your LLM provider's requirements\n"+
					"5. Confirm the base URL (%s) is correct for your LLM provider",
					errorMsg, o.llmBaseURL)
			}
		}

		return "", fmt.Errorf("LLM API returned status %d: %s", resp.StatusCode, errorMsg)
	}

	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	if len(response.Choices) == 0 {
		return "", fmt.Errorf("no response from LLM")
	}

	return response.Choices[0].Message.Content, nil
}

