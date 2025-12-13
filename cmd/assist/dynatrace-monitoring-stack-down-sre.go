package assist

import (
	"bufio"
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
	dt "github.com/openshift/osdctl/cmd/dynatrace"
	"github.com/spf13/cobra"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
)

//go:embed prompts/dynatrace_monitoring_stack_down_analysis.md
var dynatraceSystemPromptTemplate string

type dynatraceMonitoringStackDownOptions struct {
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

// NewCmdDynatraceMonitoringStackDownSRE implements the dynatrace-monitoring-stack-down-sre command
func NewCmdDynatraceMonitoringStackDownSRE() *cobra.Command {
	ops := newDynatraceMonitoringStackDownOptions()
	cmd := &cobra.Command{
		Use:   "dynatrace-monitoring-stack-down-sre",
		Short: "Collect diagnostic information for DynatraceMonitoringStackDownSRE alert",
		Long: `Collects all diagnostic information needed to troubleshoot the DynatraceMonitoringStackDownSRE alert.

This command gathers comprehensive diagnostic data including:
  - Cluster creation timestamp (to check if installation is still in progress)
  - Deployments in dynatrace namespace (operator, webhook, OTEL)
  - StatefulSets for ActiveGate
  - DaemonSets for OneAgent
  - Pods in dynatrace namespace with status
  - Pod logs for all Dynatrace components
  - Events in dynatrace namespace
  - Resource descriptions for troubleshooting

All diagnostic files are saved to a timestamped directory and a summary report
is generated. The output can optionally be archived as a tarball.

The command requires:
  - OpenShift CLI (oc) to be installed and available in PATH
  - Active cluster connection (via 'ocm backplane login')

For troubleshooting steps, refer to:
  ~/ops-sop/dynatrace/alerts/DynatraceMonitoringStackDownSRE.md
`,
		Example: `  # Collect diagnostics with default output directory
  osdctl assist dynatrace-monitoring-stack-down-sre

  # Collect diagnostics to a custom directory
  osdctl assist dynatrace-monitoring-stack-down-sre --output-dir /tmp/my-diagnostics

  # Collect diagnostics and analyze with LLM
  osdctl assist dynatrace-monitoring-stack-down-sre --analyze

  # Analyze existing directory of diagnostic artifacts
  osdctl assist dynatrace-monitoring-stack-down-sre --analyze-existing /path/to/existing-diagnostics`,
		Args:              cobra.NoArgs,
		DisableAutoGenTag: true,
		Run: func(cmd *cobra.Command, args []string) {
			cmdutil.CheckErr(ops.complete(cmd, args))
			cmdutil.CheckErr(ops.run())
		},
	}

	cmd.Flags().StringVar(&ops.outputDir, "output-dir", "", "Output directory for diagnostic files (default: dynatrace-monitoring-stack-down-diagnostics-TIMESTAMP)")
	cmd.Flags().StringVar(&ops.existingDir, "analyze-existing", "", "Path to existing directory of diagnostic artifacts to analyze with LLM (skips collection)")
	cmd.Flags().BoolVar(&ops.enableLLMAnalysis, "analyze", false, "Enable LLM analysis of collected diagnostic files")
	cmd.Flags().StringVar(&ops.llmAPIKey, "llm-api-key", "", "LLM API key (default: checks ~/.config/osdctl OPENAI_API_KEY, then env vars: LLM_API_KEY, OPENAI_API_KEY, etc.)")
	cmd.Flags().StringVar(&ops.llmBaseURL, "llm-base-url", "", "LLM API base URL (default: checks ~/.config/osdctl OPENAI_BASE_URL, then env vars, or https://api.openai.com/v1)")
	cmd.Flags().StringVar(&ops.llmModel, "llm-model", "gpt-4o-mini", "LLM model to use for analysis (default: checks ~/.config/osdctl AI_MODEL_NAME, then env vars, or gpt-4o-mini)")
	return cmd
}

func newDynatraceMonitoringStackDownOptions() *dynatraceMonitoringStackDownOptions {
	return &dynatraceMonitoringStackDownOptions{
		commandExecutor: defaultCommandExecutor,
		commandRunner:   defaultCommandRunner,
	}
}

func (o *dynatraceMonitoringStackDownOptions) complete(cmd *cobra.Command, _ []string) error {
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
		o.outputDir = fmt.Sprintf("dynatrace-monitoring-stack-down-diagnostics-%s", timestamp)
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

func (o *dynatraceMonitoringStackDownOptions) run() error {
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
		green.Println("Collecting diagnostic information for DynatraceMonitoringStackDownSRE alert...")
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
		if err := o.collectDeployments(yellow, green, red); err != nil {
			return err
		}
		if err := o.collectStatefulSets(yellow, green, red); err != nil {
			return err
		}
		if err := o.collectDaemonSets(yellow, green, red); err != nil {
			return err
		}
		if err := o.collectPods(yellow, green, red); err != nil {
			return err
		}
		if err := o.collectEvents(yellow, green, red); err != nil {
			return err
		}
		if err := o.collectComponentLogs(yellow, green, red); err != nil {
			return err
		}
		if err := o.collectClusterCreationTimestamp(yellow, green, red); err != nil {
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
			analysis, conversationHistory, err := o.analyzeWithLLM(diagnosticContent)
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

				// Interactive follow-up questions
				if err := o.interactiveFollowUp(conversationHistory, green, yellow); err != nil {
					fmt.Printf("Warning: Interactive follow-up failed: %v\n", err)
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
		fmt.Println("4. Check pod logs and describe output for error details")
		fmt.Println("5. Refer to ~/ops-sop/dynatrace/alerts/DynatraceMonitoringStackDownSRE.md for troubleshooting steps")
	} else {
		fmt.Println("3. Check pod logs and describe output for error details")
		fmt.Println("4. Refer to ~/ops-sop/dynatrace/alerts/DynatraceMonitoringStackDownSRE.md for troubleshooting steps")
		fmt.Println("5. Use --analyze flag to enable LLM analysis")
	}
	fmt.Println()

	return nil
}

func (o *dynatraceMonitoringStackDownOptions) getClusterID() (string, error) {
	output, err := o.commandRunner("get", []string{"clusterversion", "version", "-o", "jsonpath={.spec.clusterID}"})
	if err != nil || strings.TrimSpace(output) == "" {
		return "N/A", nil
	}
	return strings.TrimSpace(output), nil
}

func (o *dynatraceMonitoringStackDownOptions) collectDeployments(yellow, green, red *color.Color) error {
	safeColorPrintln(yellow, "Collecting: Deployments in dynatrace namespace")
	if err := o.runOCCommand("get", []string{"deploy", "-n", "dynatrace", "-o", "wide"}, filepath.Join(o.outputDir, "01-deployments.txt")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect deployments\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 01-deployments.txt\n\n")
	}

	safeColorPrintln(yellow, "Collecting: Deployments in dynatrace namespace (yaml)")
	if err := o.runOCCommand("get", []string{"deploy", "-n", "dynatrace", "-o", "yaml"}, filepath.Join(o.outputDir, "01-deployments.yaml")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect deployments yaml\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 01-deployments.yaml\n\n")
	}
	return nil
}

func (o *dynatraceMonitoringStackDownOptions) collectStatefulSets(yellow, green, red *color.Color) error {
	safeColorPrintln(yellow, "Collecting: StatefulSets for ActiveGate in dynatrace namespace")
	if err := o.runOCCommand("get", []string{"sts", "-n", "dynatrace", "-l", "app.kubernetes.io/component=activegate", "-o", "wide"}, filepath.Join(o.outputDir, "02-statefulsets-activegate.txt")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect ActiveGate StatefulSets\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 02-statefulsets-activegate.txt\n\n")
	}

	safeColorPrintln(yellow, "Collecting: StatefulSets for ActiveGate (yaml)")
	if err := o.runOCCommand("get", []string{"sts", "-n", "dynatrace", "-l", "app.kubernetes.io/component=activegate", "-o", "yaml"}, filepath.Join(o.outputDir, "02-statefulsets-activegate.yaml")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect ActiveGate StatefulSets yaml\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 02-statefulsets-activegate.yaml\n\n")
	}
	return nil
}

func (o *dynatraceMonitoringStackDownOptions) collectDaemonSets(yellow, green, red *color.Color) error {
	safeColorPrintln(yellow, "Collecting: DaemonSets for OneAgent in dynatrace namespace")
	if err := o.runOCCommand("get", []string{"ds", "-n", "dynatrace", "-l", "app.kubernetes.io/name=oneagent", "-o", "wide"}, filepath.Join(o.outputDir, "03-daemonsets-oneagent.txt")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect OneAgent DaemonSets\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 03-daemonsets-oneagent.txt\n\n")
	}

	safeColorPrintln(yellow, "Collecting: DaemonSets for OneAgent (yaml)")
	if err := o.runOCCommand("get", []string{"ds", "-n", "dynatrace", "-l", "app.kubernetes.io/name=oneagent", "-o", "yaml"}, filepath.Join(o.outputDir, "03-daemonsets-oneagent.yaml")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect OneAgent DaemonSets yaml\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 03-daemonsets-oneagent.yaml\n\n")
	}
	return nil
}

func (o *dynatraceMonitoringStackDownOptions) collectPods(yellow, green, red *color.Color) error {
	safeColorPrintln(yellow, "Collecting: Pods in dynatrace namespace")
	if err := o.runOCCommand("get", []string{"pod", "-n", "dynatrace", "-o", "wide"}, filepath.Join(o.outputDir, "04-pods.txt")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect pods\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 04-pods.txt\n\n")
	}

	safeColorPrintln(yellow, "Collecting: Pods in dynatrace namespace (yaml)")
	if err := o.runOCCommand("get", []string{"pod", "-n", "dynatrace", "-o", "yaml"}, filepath.Join(o.outputDir, "04-pods.yaml")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect pods yaml\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 04-pods.yaml\n\n")
	}

	// Get failing pods and collect describe output
	output, _ := o.runOCCommandWithOutput("get", []string{"pod", "-n", "dynatrace", "-o", "json"})
	var podList struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		} `json:"items"`
	}

	var failingPods []string
	if err := json.Unmarshal([]byte(output), &podList); err == nil {
		for _, pod := range podList.Items {
			if pod.Status.Phase != "Succeeded" && pod.Status.Phase != "Running" {
				failingPods = append(failingPods, pod.Metadata.Name)
			}
		}
	}

	// Collect describe output for failing pods
	for _, pod := range failingPods {
		safeColorPrintf(yellow, "Collecting: Describe output for pod %s\n", pod)
		describeFile := filepath.Join(o.outputDir, fmt.Sprintf("05-pod-describe-%s.txt", pod))
		if err := o.runOCCommand("describe", []string{"pod", pod, "-n", "dynatrace"}, describeFile); err != nil {
			safeColorPrintf(red, "  ✗ Failed to collect describe for pod %s\n\n", pod)
		} else {
			safeColorPrintf(green, "  ✓ Saved to 05-pod-describe-%s.txt\n\n", pod)
		}
	}

	return nil
}

func (o *dynatraceMonitoringStackDownOptions) collectEvents(yellow, green, red *color.Color) error {
	safeColorPrintln(yellow, "Collecting: Events in dynatrace namespace")
	if err := o.runOCCommand("get", []string{"events", "-n", "dynatrace", "--sort-by=.lastTimestamp"}, filepath.Join(o.outputDir, "06-events.txt")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect events\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 06-events.txt\n\n")
	}
	return nil
}

func (o *dynatraceMonitoringStackDownOptions) collectComponentLogs(yellow, green, red *color.Color) error {
	// Get cluster ID for Dynatrace queries
	clusterID, _ := o.getClusterID()
	if clusterID == "N/A" || clusterID == "" {
		safeColorPrintln(yellow, "Warning: Could not determine cluster ID, falling back to oc logs")
		return o.collectComponentLogsWithOC(yellow, green, red)
	}

	// Fetch cluster details for Dynatrace
	hcpCluster, err := dt.FetchClusterDetails(clusterID)
	if err != nil {
		safeColorPrintf(yellow, "Warning: Could not fetch Dynatrace cluster details (%v), falling back to oc logs\n", err)
		return o.collectComponentLogsWithOC(yellow, green, red)
	}

	// Get Dynatrace access token
	accessToken, err := dt.GetStorageAccessToken()
	if err != nil {
		safeColorPrintf(yellow, "Warning: Could not get Dynatrace access token (%v), falling back to oc logs\n", err)
		return o.collectComponentLogsWithOC(yellow, green, red)
	}

	// Collect logs for operator, webhook, and OTEL components using Dynatrace
	components := []struct {
		componentName string
		name          string
	}{
		{"operator", "operator"},
		{"webhook", "webhook"},
		{"otel", "otel"},
		{"activegate", "activegate"},
		{"oneagent", "oneagent"},
	}

	for _, comp := range components {
		safeColorPrintf(yellow, "Collecting: Logs for %s component (from Dynatrace)\n", comp.name)
		logFile := filepath.Join(o.outputDir, fmt.Sprintf("07-logs-%s.txt", comp.name))

		// Build Dynatrace query for this component
		query := &dt.DTQuery{}
		query.InitLogs(24).Cluster(hcpCluster.ManagementClusterName()).Namespaces([]string{"dynatrace"})

		// Filter by component using workload name or container name
		// For operator, webhook, otel: use workload name
		// For activegate, oneagent: use container name pattern
		if comp.componentName == "activegate" {
			query.Containers([]string{"activegate"})
		} else if comp.componentName == "oneagent" {
			query.Containers([]string{"oneagent"})
		} else {
			// For operator, webhook, otel - try to match by workload name
			workloadName := fmt.Sprintf("dynatrace-%s", comp.componentName)
			query.Deployments([]string{workloadName})
		}

		query.Limit(10000)
		sortedQuery, err := query.Sort("desc")
		if err != nil {
			safeColorPrintf(red, "  ✗ Failed to build query for %s: %v\n\n", comp.name, err)
			continue
		}

		// Execute query and get logs
		requestToken, err := dt.GetDTQueryExecution(hcpCluster.DynatraceURL, accessToken, sortedQuery.Build())
		if err != nil {
			safeColorPrintf(red, "  ✗ Failed to execute Dynatrace query for %s: %v\n\n", comp.name, err)
			continue
		}

		// Open file for writing
		file, err := os.Create(logFile)
		if err != nil {
			safeColorPrintf(red, "  ✗ Failed to create log file for %s: %v\n\n", comp.name, err)
			continue
		}

		// Get logs and write to file
		err = dt.GetLogs(hcpCluster.DynatraceURL, accessToken, requestToken, file)
		file.Close()

		if err != nil {
			safeColorPrintf(red, "  ✗ Failed to collect logs for %s: %v\n\n", comp.name, err)
			// Remove empty file on error
			os.Remove(logFile)
		} else {
			safeColorPrintf(green, "  ✓ Saved to 07-logs-%s.txt\n\n", comp.name)
		}
	}

	return nil
}

// collectComponentLogsWithOC is a fallback method using oc logs
func (o *dynatraceMonitoringStackDownOptions) collectComponentLogsWithOC(yellow, green, red *color.Color) error {
	// Collect logs for operator, webhook, and OTEL components
	components := []struct {
		label      string
		labelValue string
		name       string
	}{
		{"app.kubernetes.io/component", "operator", "operator"},
		{"app.kubernetes.io/component", "webhook", "webhook"},
		{"app.kubernetes.io/component", "otel", "otel"},
		{"app.kubernetes.io/component", "activegate", "activegate"},
		{"app.kubernetes.io/name", "oneagent", "oneagent"},
	}

	for _, comp := range components {
		safeColorPrintf(yellow, "Collecting: Logs for %s component (using oc logs)\n", comp.name)
		logFile := filepath.Join(o.outputDir, fmt.Sprintf("07-logs-%s.txt", comp.name))
		labelSelector := fmt.Sprintf("%s=%s", comp.label, comp.labelValue)
		if err := o.runOCCommand("logs", []string{"-n", "dynatrace", "-l", labelSelector, "--tail=-1"}, logFile); err != nil {
			safeColorPrintf(red, "  ✗ Failed to collect logs for %s\n\n", comp.name)
		} else {
			safeColorPrintf(green, "  ✓ Saved to 07-logs-%s.txt\n\n", comp.name)
		}
	}

	return nil
}

func (o *dynatraceMonitoringStackDownOptions) collectClusterCreationTimestamp(yellow, green, red *color.Color) error {
	safeColorPrintln(yellow, "Collecting: Cluster creation timestamp (to check if installation is still in progress)")
	// Note: This requires ocm CLI, but we'll try to get it from cluster version or note it in the summary
	// The actual ocm command would be: ocm get cluster $MC_CLUSTER_ID | jq .creation_timestamp
	noteFile := filepath.Join(o.outputDir, "00-cluster-creation-note.txt")
	note := `Note: To check cluster creation timestamp, run:
ocm get cluster $MC_CLUSTER_ID | jq .creation_timestamp

If the creation timestamp is about 15-20 mins and this alert is fired,
it may be because the installation is still going on.
`
	if err := o.writeFile(noteFile, note); err != nil {
		safeColorPrintf(red, "  ✗ Failed to create cluster creation note\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 00-cluster-creation-note.txt\n\n")
	}
	return nil
}

func (o *dynatraceMonitoringStackDownOptions) runOCCommand(subcommand string, args []string, outputFile string) error {
	return o.commandExecutor(subcommand, args, outputFile)
}

func (o *dynatraceMonitoringStackDownOptions) runOCCommandWithOutput(subcommand string, args []string) (string, error) {
	return o.commandRunner(subcommand, args)
}

func (o *dynatraceMonitoringStackDownOptions) writeFile(filePath string, content string) error {
	return os.WriteFile(filePath, []byte(content), 0644)
}

func (o *dynatraceMonitoringStackDownOptions) generateSummary(clusterID string, green *color.Color) error {
	safeColorPrintln(green, "Generating summary report...")

	summaryFile := filepath.Join(o.outputDir, "00-SUMMARY.txt")
	summary := fmt.Sprintf(`DynatraceMonitoringStackDownSRE Diagnostic Collection Summary
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

	// Add deployments status
	deployFile := filepath.Join(o.outputDir, "01-deployments.txt")
	if content, err := os.ReadFile(deployFile); err == nil {
		summary += "\nDeployments Status:\n"
		summary += string(content) + "\n"
	}

	// Add pods status
	podsFile := filepath.Join(o.outputDir, "04-pods.txt")
	if content, err := os.ReadFile(podsFile); err == nil {
		summary += "\nPods Status:\n"
		summary += string(content) + "\n"
	}

	// Add ActiveGate StatefulSet status
	stsFile := filepath.Join(o.outputDir, "02-statefulsets-activegate.txt")
	if content, err := os.ReadFile(stsFile); err == nil {
		summary += "\nActiveGate StatefulSet Status:\n"
		summary += string(content) + "\n"
	}

	// Add OneAgent DaemonSet status
	dsFile := filepath.Join(o.outputDir, "03-daemonsets-oneagent.txt")
	if content, err := os.ReadFile(dsFile); err == nil {
		summary += "\nOneAgent DaemonSet Status:\n"
		summary += string(content) + "\n"
	}

	return o.writeFile(summaryFile, summary)
}

func (o *dynatraceMonitoringStackDownOptions) createTarball() error {
	cmd := exec.Command("tar", "-czf", o.outputDir+".tar.gz", o.outputDir)
	return cmd.Run()
}

// extractAndReadDiagnostics extracts key diagnostic files and returns their content
func (o *dynatraceMonitoringStackDownOptions) extractAndReadDiagnostics() (string, error) {
	var content strings.Builder

	// Priority files to read (in order)
	priorityFiles := []string{
		"00-SUMMARY.txt",
		"01-deployments.txt",
		"02-statefulsets-activegate.txt",
		"03-daemonsets-oneagent.txt",
		"04-pods.txt",
		"06-events.txt",
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
		"01-deployments.yaml",
		"02-statefulsets-activegate.yaml",
		"03-daemonsets-oneagent.yaml",
		"04-pods.yaml",
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

	// Read pod describe files (limit to first 5)
	podDescribeFiles, _ := filepath.Glob(filepath.Join(o.outputDir, "05-pod-describe-*.txt"))
	sort.Strings(podDescribeFiles)
	if len(podDescribeFiles) > 5 {
		podDescribeFiles = podDescribeFiles[:5]
	}

	for _, filePath := range podDescribeFiles {
		if data, err := os.ReadFile(filePath); err == nil {
			content.WriteString(fmt.Sprintf("\n=== %s ===\n", filepath.Base(filePath)))
			content.Write(data)
			content.WriteString("\n")
		}
	}

	// Read component logs (limit size)
	logFiles := []string{
		"07-logs-operator.txt",
		"07-logs-webhook.txt",
		"07-logs-otel.txt",
		"07-logs-activegate.txt",
		"07-logs-oneagent.txt",
	}

	for _, filename := range logFiles {
		filePath := filepath.Join(o.outputDir, filename)
		if data, err := os.ReadFile(filePath); err == nil {
			content.WriteString(fmt.Sprintf("\n=== %s ===\n", filename))
			logContent := string(data)
			if len(logContent) > 10000 {
				logContent = logContent[:10000] + "\n... (truncated)"
			}
			content.WriteString(logContent)
			content.WriteString("\n")
		}
	}

	return content.String(), nil
}

// analyzeWithLLM sends diagnostic content to LLM for analysis and returns the response and conversation history
func (o *dynatraceMonitoringStackDownOptions) analyzeWithLLM(diagnosticContent string) (string, []Message, error) {
	systemPrompt := dynatraceSystemPromptTemplate
	// Fallback to default if embed failed (shouldn't happen, but be safe)
	if systemPrompt == "" {
		systemPrompt = `You are an expert OpenShift/Kubernetes Site Reliability Engineer (SRE) specializing in Dynatrace monitoring stack. Your task is to analyze diagnostic information and assess the health of Dynatrace components (Operator, Webhook, OneAgent, ActiveGate, OTEL) on any OpenShift cluster.

Analyze the provided diagnostic data and provide:
1. Root cause analysis - What is likely causing the Dynatrace stack to be down?
2. Key findings - What are the most important issues identified?
3. Recommended actions - What steps should be taken to resolve the issues?
4. Priority - Rate the severity (Critical/High/Medium/Low/Healthy)

Be concise but thorough. Focus on actionable insights.`
	}

	// Build conversation history
	conversationHistory := []Message{
		{
			Role:    "system",
			Content: systemPrompt,
		},
		{
			Role:    "user",
			Content: fmt.Sprintf("Please analyze the following DynatraceMonitoringStackDownSRE diagnostic information from an OpenShift cluster:\n\n%s", diagnosticContent),
		},
	}

	requestBody := map[string]interface{}{
		"model":       o.llmModel,
		"messages":    conversationHistory,
		"temperature": 0.3,
		"max_tokens":  4000,
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return "", nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Construct URL - ensure base URL doesn't have trailing slash, and endpoint does
	baseURL := strings.TrimSuffix(o.llmBaseURL, "/")
	endpoint := "/chat/completions"
	url := baseURL + endpoint

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", nil, fmt.Errorf("failed to create request: %w", err)
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
		return "", nil, fmt.Errorf("failed to send request: %w", err)
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
				return "", nil, fmt.Errorf("authentication failed (401): %s\n\nTroubleshooting:\n"+
					"1. Verify your API key is correct\n"+
					"2. Ensure there are no extra spaces or newlines in the key\n"+
					"3. Check your environment variables: LLM_API_KEY, OPENAI_API_KEY, etc.\n"+
					"4. Verify the API key format matches your LLM provider's requirements\n"+
					"5. Confirm the base URL (%s) is correct for your LLM provider",
					errorMsg, o.llmBaseURL)
			}
		}

		return "", nil, fmt.Errorf("LLM API returned status %d: %s", resp.StatusCode, errorMsg)
	}

	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return "", nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(response.Choices) == 0 {
		return "", nil, fmt.Errorf("no response from LLM")
	}

	assistantResponse := response.Choices[0].Message.Content

	// Add assistant response to conversation history
	conversationHistory = append(conversationHistory, Message{
		Role:    "assistant",
		Content: assistantResponse,
	})

	return assistantResponse, conversationHistory, nil
}

// askFollowUpQuestion sends a follow-up question to the LLM with conversation history
func (o *dynatraceMonitoringStackDownOptions) askFollowUpQuestion(conversationHistory []Message, question string) (string, []Message, error) {
	// Add user question to conversation history
	conversationHistory = append(conversationHistory, Message{
		Role:    "user",
		Content: question,
	})

	requestBody := map[string]interface{}{
		"model":       o.llmModel,
		"messages":    conversationHistory,
		"temperature": 0.3,
		"max_tokens":  4000,
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return "", conversationHistory, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Construct URL
	baseURL := strings.TrimSuffix(o.llmBaseURL, "/")
	endpoint := "/chat/completions"
	url := baseURL + endpoint

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", conversationHistory, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	apiKey := strings.TrimSpace(o.llmAPIKey)
	apiKey = strings.ReplaceAll(apiKey, "\n", "")
	apiKey = strings.ReplaceAll(apiKey, "\r", "")

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{
		Timeout: 120 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", conversationHistory, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		var errorResp struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}

		errorMsg := string(body)
		if err := json.Unmarshal(body, &errorResp); err == nil && errorResp.Error.Message != "" {
			errorMsg = errorResp.Error.Message
		}

		return "", conversationHistory, fmt.Errorf("LLM API returned status %d: %s", resp.StatusCode, errorMsg)
	}

	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return "", conversationHistory, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(response.Choices) == 0 {
		return "", conversationHistory, fmt.Errorf("no response from LLM")
	}

	assistantResponse := response.Choices[0].Message.Content

	// Add assistant response to conversation history
	conversationHistory = append(conversationHistory, Message{
		Role:    "assistant",
		Content: assistantResponse,
	})

	return assistantResponse, conversationHistory, nil
}

// interactiveFollowUp handles interactive follow-up questions
func (o *dynatraceMonitoringStackDownOptions) interactiveFollowUp(conversationHistory []Message, green, yellow *color.Color) error {
	scanner := bufio.NewScanner(os.Stdin)

	yellow.Println("\n=== Interactive Follow-up ===")
	fmt.Println("You can ask follow-up questions about the analysis. Type 'exit' or 'quit' to finish.")
	fmt.Println()

	for {
		green.Print("Question (or 'exit' to finish): ")

		if !scanner.Scan() {
			// Handle EOF (Ctrl+D)
			if err := scanner.Err(); err != nil {
				return fmt.Errorf("error reading input: %w", err)
			}
			break
		}

		question := strings.TrimSpace(scanner.Text())

		// Check for exit commands
		if question == "" {
			continue
		}
		if strings.ToLower(question) == "exit" || strings.ToLower(question) == "quit" || strings.ToLower(question) == "q" {
			green.Println("Exiting interactive mode.")
			break
		}

		// Ask the follow-up question
		yellow.Println("\nThinking...")
		response, updatedHistory, err := o.askFollowUpQuestion(conversationHistory, question)
		if err != nil {
			fmt.Printf("Error: Failed to get response: %v\n", err)
			continue
		}

		// Update conversation history
		conversationHistory = updatedHistory

		// Display response
		green.Println("\n=== Response ===")
		fmt.Println(response)
		fmt.Println()

		// Append to analysis file
		analysisFile := filepath.Join(o.outputDir, "08-llm-analysis.txt")
		appendContent := fmt.Sprintf("\n\n=== Follow-up Question ===\n%s\n\n=== Response ===\n%s\n", question, response)
		if err := o.appendToFile(analysisFile, appendContent); err != nil {
			fmt.Printf("Warning: Failed to append follow-up to analysis file: %v\n", err)
		}
	}

	return nil
}

// appendToFile appends content to a file
func (o *dynatraceMonitoringStackDownOptions) appendToFile(filePath string, content string) error {
	file, err := os.OpenFile(filePath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.WriteString(content)
	return err
}
