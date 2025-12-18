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
	"github.com/spf13/cobra"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
)

//go:embed prompts/cluster_provisioning_failure_analysis.md
var clusterProvisioningSystemPromptTemplate string

type clusterProvisioningFailureOptions struct {
	outputDir         string
	existingDir       string // Directory with existing artifacts to analyze
	skipCollection    bool   // Skip collection if analyzing existing directory
	clusterID         string // External cluster ID (will be used to find internal ID)
	clusterInternalID string // Internal ID of the cluster for install logs collection (resolved from clusterID)
	// LLM analysis options
	enableLLMAnalysis bool
	llmAPIKey         string
	llmBaseURL        string
	llmModel          string
	// For testing: allows injection of command executor
	commandExecutor func(name string, args []string, outputFile string) error
	commandRunner   func(name string, args []string) (string, error)
}

// NewCmdClusterProvisioningFailure implements the cluster-provisioning-failure command
func NewCmdClusterProvisioningFailure() *cobra.Command {
	ops := newClusterProvisioningFailureOptions()
	cmd := &cobra.Command{
		Use:   "cluster-provisioning-failure",
		Short: "Collect diagnostic information for ClusterProvisioningFailure alert",
		Long: `Collects all diagnostic information needed to troubleshoot the ClusterProvisioningFailure alert.

IMPORTANT: For failed installations where the cluster is not accessible, provide the 
cluster ID with --cluster flag. The command will automatically resolve the internal ID
and provide install logs collection instructions.

This command gathers comprehensive diagnostic data including:
  - Cluster version and installation status
  - ClusterOperators status (focusing on failing operators)
  - Machine sets and machines in all namespaces
  - Nodes status and conditions
  - Infrastructure configuration
  - Install configuration
  - Events across critical namespaces
  - Logs from failing cluster operators
  - Pod status in critical installation namespaces
  - Install logs collection instructions (via OCM)

All diagnostic files are saved to a timestamped directory and a summary report
is generated. The output can optionally be archived as a tarball.

The command requires:
  - OpenShift CLI (oc) to be installed and available in PATH (if cluster is accessible)
  - OCM CLI for install logs collection (if cluster is not accessible)
  - Active cluster connection (via 'ocm backplane login') or cluster internal ID

For troubleshooting steps, refer to:
  https://github.com/openshift/ops-sop/blob/master/v4/alerts/ClusterProvisioningFailure.md
`,
		Example: `  # Collect diagnostics with cluster ID (recommended for failed installations)
  osdctl assist cluster-provisioning-failure --cluster $CLUSTER_ID

  # Collect diagnostics with default output directory
  osdctl assist cluster-provisioning-failure

  # Collect diagnostics to a custom directory
  osdctl assist cluster-provisioning-failure --output-dir /tmp/my-diagnostics

  # Collect diagnostics and analyze with LLM
  osdctl assist cluster-provisioning-failure --analyze --cluster $CLUSTER_ID

  # Analyze existing directory of diagnostic artifacts
  osdctl assist cluster-provisioning-failure --analyze-existing /path/to/existing-diagnostics`,
		Args:              cobra.NoArgs,
		DisableAutoGenTag: true,
		Run: func(cmd *cobra.Command, args []string) {
			cmdutil.CheckErr(ops.complete(cmd, args))
			cmdutil.CheckErr(ops.run())
		},
	}

	cmd.Flags().StringVar(&ops.outputDir, "output-dir", "", "Output directory for diagnostic files (default: cluster-provisioning-failure-diagnostics-TIMESTAMP)")
	cmd.Flags().StringVar(&ops.existingDir, "analyze-existing", "", "Path to existing directory of diagnostic artifacts to analyze with LLM (skips collection)")
	cmd.Flags().StringVar(&ops.clusterID, "cluster", "", "Cluster ID (external) - will be used to find internal ID for install logs collection via OCM")
	cmd.Flags().BoolVar(&ops.enableLLMAnalysis, "analyze", false, "Enable LLM analysis of collected diagnostic files")
	cmd.Flags().StringVar(&ops.llmAPIKey, "llm-api-key", "", "LLM API key (default: checks ~/.config/osdctl OPENAI_API_KEY, then env vars: LLM_API_KEY, OPENAI_API_KEY, etc.)")
	cmd.Flags().StringVar(&ops.llmBaseURL, "llm-base-url", "", "LLM API base URL (default: checks ~/.config/osdctl OPENAI_BASE_URL, then env vars, or https://api.openai.com/v1)")
	cmd.Flags().StringVar(&ops.llmModel, "llm-model", "gpt-4o-mini", "LLM model to use for analysis (default: checks ~/.config/osdctl AI_MODEL_NAME, then env vars, or gpt-4o-mini)")
	return cmd
}

func newClusterProvisioningFailureOptions() *clusterProvisioningFailureOptions {
	return &clusterProvisioningFailureOptions{
		commandExecutor: defaultCommandExecutor,
		commandRunner:   defaultCommandRunner,
	}
}

func (o *clusterProvisioningFailureOptions) complete(cmd *cobra.Command, _ []string) error {
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
		o.outputDir = fmt.Sprintf("cluster-provisioning-failure-diagnostics-%s", timestamp)
	}

	// If cluster ID provided, resolve internal ID
	if o.clusterID != "" {
		internalID, err := o.resolveInternalID(o.clusterID)
		if err != nil {
			fmt.Printf("Warning: Failed to resolve internal ID from cluster %s: %v\n", o.clusterID, err)
			fmt.Println("You can manually find the internal ID with: ocm describe cluster", o.clusterID)
		} else {
			o.clusterInternalID = internalID
		}
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

// resolveInternalID uses ocm describe cluster to get the internal ID from external cluster ID
func (o *clusterProvisioningFailureOptions) resolveInternalID(clusterID string) (string, error) {
	// Run: ocm describe cluster <clusterID> --json
	cmd := exec.Command("ocm", "describe", "cluster", clusterID, "--json")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to run ocm describe cluster: %w (output: %s)", err, string(output))
	}

	// Parse JSON to get internal ID
	var clusterInfo struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(output, &clusterInfo); err != nil {
		return "", fmt.Errorf("failed to parse ocm output: %w", err)
	}

	if clusterInfo.ID == "" {
		return "", fmt.Errorf("internal ID not found in ocm output")
	}

	return clusterInfo.ID, nil
}

func (o *clusterProvisioningFailureOptions) run() error {
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
		green.Println("Collecting diagnostic information for ClusterProvisioningFailure alert...")
		fmt.Printf("Output directory: %s\n\n", o.outputDir)

		// Create output directory
		if err := os.MkdirAll(o.outputDir, 0755); err != nil {
			return fmt.Errorf("failed to create output directory: %w", err)
		}

		// For cluster provisioning failures, the cluster is typically not accessible
		// Focus on collecting install logs via OCM
		yellow.Println("NOTE: For cluster provisioning failures, the cluster is typically not accessible.")
		yellow.Println("This command will collect install logs via OCM.")
		fmt.Println()

		// Validate cluster ID is provided
		if o.clusterID == "" {
			red.Println("Error: --cluster flag is required for cluster provisioning failures.")
			fmt.Println("\nUsage:")
			fmt.Println("  osdctl assist cluster-provisioning-failure --cluster $CLUSTER_ID")
			fmt.Println("\nTo find your cluster ID:")
			fmt.Println("  ocm list clusters")
			return fmt.Errorf("cluster ID required")
		}

		// Show cluster information
		green.Println("Cluster Information:")
		fmt.Printf("Cluster ID: %s\n", o.clusterID)
		if o.clusterInternalID != "" {
			fmt.Printf("Internal ID: %s\n", o.clusterInternalID)
		}
		fmt.Println()

		// Create install logs collection note
		if err := o.createInstallLogsNote(yellow, green); err != nil {
			// Non-fatal, continue
			fmt.Printf("Warning: Failed to create install logs note: %v\n", err)
		}

		// Collect install logs via OCM
		if err := o.collectInstallLogs(yellow, green, red); err != nil {
			// Non-fatal - continue to generate instructions even if collection fails
			fmt.Printf("Warning: Failed to collect install logs: %v\n", err)
			yellow.Println("See 00-INSTALL-LOGS-COLLECTION.txt for manual collection instructions.")
		}

		// Collect cluster information via OCM
		if err := o.collectClusterInfoViaOCM(yellow, green, red); err != nil {
			// Non-fatal
			fmt.Printf("Warning: Failed to collect cluster info via OCM: %v\n", err)
		}

		// Generate summary
		if err := o.generateSummaryForFailedInstall(green); err != nil {
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
				analysisFile := filepath.Join(o.outputDir, "10-llm-analysis.txt")
				if err := o.writeFile(analysisFile, analysis); err != nil {
					fmt.Printf("Warning: Failed to save LLM analysis: %v\n", err)
				} else {
					green.Printf("✓ LLM analysis saved to 10-llm-analysis.txt\n")
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
	fmt.Println("3. Review install logs in 01-install-logs.txt")
	fmt.Println("   Look for ERROR messages, permission issues, quota limits, network errors")
	fmt.Println("4. Check cluster info in 02-cluster-info-ocm.txt for cluster state and status")
	if o.enableLLMAnalysis {
		fmt.Println("5. Review 10-llm-analysis.txt for AI-powered insights")
		fmt.Println("6. Refer to https://github.com/openshift/ops-sop/blob/master/v4/alerts/ClusterProvisioningFailure.md for troubleshooting steps")
	} else {
		fmt.Println("5. Refer to https://github.com/openshift/ops-sop/blob/master/v4/alerts/ClusterProvisioningFailure.md for troubleshooting steps")
		fmt.Println("6. Use --analyze flag to enable LLM analysis")
	}
	if o.clusterInternalID != "" {
		fmt.Println("\nTo re-collect install logs manually:")
		fmt.Printf("  echo -e `ocm get /api/clusters_mgmt/v1/clusters/%s/resources | jq -r '.resources.install_logs_tail'` > install-logs.txt\n", o.clusterInternalID)
	}
	fmt.Println()

	return nil
}

func (o *clusterProvisioningFailureOptions) getClusterID() (string, error) {
	output, err := o.commandRunner("get", []string{"clusterversion", "version", "-o", "jsonpath={.spec.clusterID}"})
	if err != nil || strings.TrimSpace(output) == "" {
		return "N/A", nil
	}
	return strings.TrimSpace(output), nil
}

func (o *clusterProvisioningFailureOptions) collectClusterVersion(yellow, green, red *color.Color) error {
	safeColorPrintln(yellow, "Collecting: Cluster version and status")
	if err := o.runOCCommand("get", []string{"clusterversion", "-o", "wide"}, filepath.Join(o.outputDir, "01-clusterversion.txt")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect cluster version\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 01-clusterversion.txt\n\n")
	}

	safeColorPrintln(yellow, "Collecting: Cluster version details (yaml)")
	if err := o.runOCCommand("get", []string{"clusterversion", "version", "-o", "yaml"}, filepath.Join(o.outputDir, "01-clusterversion.yaml")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect cluster version yaml\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 01-clusterversion.yaml\n\n")
	}
	return nil
}

func (o *clusterProvisioningFailureOptions) collectClusterOperators(yellow, green, red *color.Color) error {
	safeColorPrintln(yellow, "Collecting: Cluster operators status")
	if err := o.runOCCommand("get", []string{"clusteroperators", "-o", "wide"}, filepath.Join(o.outputDir, "02-clusteroperators.txt")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect cluster operators\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 02-clusteroperators.txt\n\n")
	}

	safeColorPrintln(yellow, "Collecting: Cluster operators details (yaml)")
	if err := o.runOCCommand("get", []string{"clusteroperators", "-o", "yaml"}, filepath.Join(o.outputDir, "02-clusteroperators.yaml")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect cluster operators yaml\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 02-clusteroperators.yaml\n\n")
	}

	// Get failing/degraded cluster operators
	safeColorPrintln(yellow, "Collecting: Describe output for degraded/unavailable cluster operators")
	output, _ := o.runOCCommandWithOutput("get", []string{"clusteroperators", "-o", "json"})
	var coList struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}

	var degradedOperators []string
	if err := json.Unmarshal([]byte(output), &coList); err == nil {
		for _, co := range coList.Items {
			for _, condition := range co.Status.Conditions {
				if (condition.Type == "Degraded" && condition.Status == "True") ||
					(condition.Type == "Available" && condition.Status == "False") ||
					(condition.Type == "Progressing" && condition.Status == "True") {
					degradedOperators = append(degradedOperators, co.Metadata.Name)
					break
				}
			}
		}
	}

	for _, coName := range degradedOperators {
		safeColorPrintf(yellow, "Collecting: Describe output for cluster operator %s\n", coName)
		describeFile := filepath.Join(o.outputDir, fmt.Sprintf("03-clusteroperator-describe-%s.txt", coName))
		if err := o.runOCCommand("describe", []string{"clusteroperator", coName}, describeFile); err != nil {
			safeColorPrintf(red, "  ✗ Failed to collect describe for cluster operator %s\n\n", coName)
		} else {
			safeColorPrintf(green, "  ✓ Saved to 03-clusteroperator-describe-%s.txt\n\n", coName)
		}
	}

	return nil
}

func (o *clusterProvisioningFailureOptions) collectMachines(yellow, green, red *color.Color) error {
	safeColorPrintln(yellow, "Collecting: MachineSets in all namespaces")
	if err := o.runOCCommand("get", []string{"machinesets", "-A", "-o", "wide"}, filepath.Join(o.outputDir, "04-machinesets.txt")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect machinesets\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 04-machinesets.txt\n\n")
	}

	safeColorPrintln(yellow, "Collecting: MachineSets details (yaml)")
	if err := o.runOCCommand("get", []string{"machinesets", "-A", "-o", "yaml"}, filepath.Join(o.outputDir, "04-machinesets.yaml")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect machinesets yaml\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 04-machinesets.yaml\n\n")
	}

	safeColorPrintln(yellow, "Collecting: Machines in all namespaces")
	if err := o.runOCCommand("get", []string{"machines", "-A", "-o", "wide"}, filepath.Join(o.outputDir, "05-machines.txt")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect machines\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 05-machines.txt\n\n")
	}

	safeColorPrintln(yellow, "Collecting: Machines details (yaml)")
	if err := o.runOCCommand("get", []string{"machines", "-A", "-o", "yaml"}, filepath.Join(o.outputDir, "05-machines.yaml")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect machines yaml\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 05-machines.yaml\n\n")
	}

	return nil
}

func (o *clusterProvisioningFailureOptions) collectNodes(yellow, green, red *color.Color) error {
	safeColorPrintln(yellow, "Collecting: Nodes status")
	if err := o.runOCCommand("get", []string{"nodes", "-o", "wide"}, filepath.Join(o.outputDir, "06-nodes.txt")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect nodes\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 06-nodes.txt\n\n")
	}

	safeColorPrintln(yellow, "Collecting: Nodes details (yaml)")
	if err := o.runOCCommand("get", []string{"nodes", "-o", "yaml"}, filepath.Join(o.outputDir, "06-nodes.yaml")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect nodes yaml\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 06-nodes.yaml\n\n")
	}

	// Get not-ready nodes and describe them
	output, _ := o.runOCCommandWithOutput("get", []string{"nodes", "-o", "json"})
	var nodeList struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}

	var notReadyNodes []string
	if err := json.Unmarshal([]byte(output), &nodeList); err == nil {
		for _, node := range nodeList.Items {
			isReady := false
			for _, condition := range node.Status.Conditions {
				if condition.Type == "Ready" && condition.Status == "True" {
					isReady = true
					break
				}
			}
			if !isReady {
				notReadyNodes = append(notReadyNodes, node.Metadata.Name)
			}
		}
	}

	for _, nodeName := range notReadyNodes {
		safeColorPrintf(yellow, "Collecting: Describe output for not-ready node %s\n", nodeName)
		describeFile := filepath.Join(o.outputDir, fmt.Sprintf("07-node-describe-%s.txt", nodeName))
		if err := o.runOCCommand("describe", []string{"node", nodeName}, describeFile); err != nil {
			safeColorPrintf(red, "  ✗ Failed to collect describe for node %s\n\n", nodeName)
		} else {
			safeColorPrintf(green, "  ✓ Saved to 07-node-describe-%s.txt\n\n", nodeName)
		}
	}

	return nil
}

func (o *clusterProvisioningFailureOptions) collectInfrastructure(yellow, green, red *color.Color) error {
	safeColorPrintln(yellow, "Collecting: Infrastructure configuration")
	if err := o.runOCCommand("get", []string{"infrastructure", "cluster", "-o", "yaml"}, filepath.Join(o.outputDir, "08-infrastructure.yaml")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect infrastructure config\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 08-infrastructure.yaml\n\n")
	}

	safeColorPrintln(yellow, "Collecting: DNS configuration")
	if err := o.runOCCommand("get", []string{"dns", "cluster", "-o", "yaml"}, filepath.Join(o.outputDir, "08-dns.yaml")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect DNS config\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 08-dns.yaml\n\n")
	}

	safeColorPrintln(yellow, "Collecting: Network configuration")
	if err := o.runOCCommand("get", []string{"network", "cluster", "-o", "yaml"}, filepath.Join(o.outputDir, "08-network.yaml")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect network config\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 08-network.yaml\n\n")
	}

	return nil
}

func (o *clusterProvisioningFailureOptions) collectInstallConfig(yellow, green, red *color.Color) error {
	safeColorPrintln(yellow, "Collecting: Install config from cluster-config-v1 configmap")
	if err := o.runOCCommand("get", []string{"configmap", "cluster-config-v1", "-n", "kube-system", "-o", "yaml"}, filepath.Join(o.outputDir, "09-install-config.yaml")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect install config (may not exist on all clusters)\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 09-install-config.yaml\n\n")
	}
	return nil
}

func (o *clusterProvisioningFailureOptions) collectEvents(yellow, green, red *color.Color) error {
	// Collect events from critical namespaces
	namespaces := []string{
		"openshift-cluster-version",
		"openshift-machine-api",
		"openshift-kube-apiserver",
		"openshift-etcd",
		"openshift-authentication",
		"openshift-ingress",
		"kube-system",
	}

	for _, ns := range namespaces {
		safeColorPrintf(yellow, "Collecting: Events in namespace %s\n", ns)
		eventsFile := filepath.Join(o.outputDir, fmt.Sprintf("10-events-%s.txt", ns))
		if err := o.runOCCommand("get", []string{"events", "-n", ns, "--sort-by=.lastTimestamp"}, eventsFile); err != nil {
			safeColorPrintf(red, "  ✗ Failed to collect events in %s\n\n", ns)
		} else {
			safeColorPrintf(green, "  ✓ Saved to 10-events-%s.txt\n\n", ns)
		}
	}

	// Also collect all events
	safeColorPrintln(yellow, "Collecting: All cluster events")
	if err := o.runOCCommand("get", []string{"events", "-A", "--sort-by=.lastTimestamp"}, filepath.Join(o.outputDir, "10-events-all.txt")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect all events\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 10-events-all.txt\n\n")
	}

	return nil
}

func (o *clusterProvisioningFailureOptions) collectOperatorLogs(yellow, green, red *color.Color) error {
	// Collect logs from critical operator pods
	operators := []struct {
		namespace string
		label     string
		name      string
	}{
		{"openshift-cluster-version", "k8s-app=cluster-version-operator", "cluster-version-operator"},
		{"openshift-machine-api", "k8s-app=machine-api-operator", "machine-api-operator"},
		{"openshift-machine-api", "k8s-app=machine-api-controllers", "machine-api-controllers"},
		{"openshift-kube-apiserver-operator", "app=kube-apiserver-operator", "kube-apiserver-operator"},
		{"openshift-authentication-operator", "app=authentication-operator", "authentication-operator"},
	}

	for _, op := range operators {
		safeColorPrintf(yellow, "Collecting: Logs for %s in %s namespace\n", op.name, op.namespace)
		logFile := filepath.Join(o.outputDir, fmt.Sprintf("11-logs-%s.txt", op.name))
		if err := o.runOCCommand("logs", []string{"-n", op.namespace, "-l", op.label, "--tail=500"}, logFile); err != nil {
			safeColorPrintf(red, "  ✗ Failed to collect logs for %s\n\n", op.name)
		} else {
			safeColorPrintf(green, "  ✓ Saved to 11-logs-%s.txt\n\n", op.name)
		}
	}

	return nil
}

func (o *clusterProvisioningFailureOptions) createInstallLogsNote(yellow, green *color.Color) error {
	safeColorPrintln(yellow, "Creating install logs collection note...")

	noteFile := filepath.Join(o.outputDir, "00-INSTALL-LOGS-COLLECTION.txt")

	var note string
	if o.clusterID != "" && o.clusterInternalID != "" {
		// Generate note with both cluster ID and internal ID
		note = fmt.Sprintf("========================================\n"+
			"INSTALL LOGS COLLECTION INSTRUCTIONS\n"+
			"========================================\n\n"+
			"Cluster ID: %s\n"+
			"Internal ID: %s\n\n"+
			"For cluster provisioning failures, the cluster may not be accessible via 'oc' commands.\n"+
			"Collect install logs directly from OCM using the command below.\n\n"+
			"COMMAND TO COLLECT INSTALL LOGS:\n"+
			"---------------------------------\n\n"+
			"echo -e `ocm get /api/clusters_mgmt/v1/clusters/%s/resources | jq -r '.resources.install_logs_tail'` > install-logs.txt\n\n"+
			"QUICK START:\n"+
			"------------\n\n"+
			"1. Run the command above to save install logs to install-logs.txt\n\n"+
			"2. Review the logs for error messages:\n"+
			"   less install-logs.txt\n\n"+
			"   Look for:\n"+
			"   - ERROR level messages\n"+
			"   - Failed operations\n"+
			"   - Timeout errors\n"+
			"   - Permission/authorization errors\n"+
			"   - Resource provisioning failures\n"+
			"   - Network connectivity issues\n\n"+
			"COMMON INSTALL FAILURE PATTERNS:\n"+
			"---------------------------------\n\n"+
			"1. AWS: IAM permission issues, instance limits, EBS volume limits\n"+
			"2. Azure: Subscription limits, resource provider not registered\n"+
			"3. GCP: API not enabled, service account permissions\n"+
			"4. Network: VPC/subnet configuration, DNS resolution failures\n"+
			"5. Resources: Insufficient quota, unavailable instance types\n\n"+
			"ADDITIONAL OCM COMMANDS:\n"+
			"------------------------\n\n"+
			"# Get cluster status (using cluster ID)\n"+
			"ocm describe cluster %s\n\n"+
			"# Get cluster details (JSON)\n"+
			"ocm describe cluster %s --json\n\n"+
			"# Get cluster events (using internal ID)\n"+
			"ocm get /api/clusters_mgmt/v1/clusters/%s/events\n\n"+
			"# Get cluster installation status (using internal ID)\n"+
			"ocm get /api/clusters_mgmt/v1/clusters/%s | jq '.status'\n\n"+
			"========================================\n",
			o.clusterID, o.clusterInternalID, o.clusterInternalID, o.clusterID, o.clusterID, o.clusterInternalID, o.clusterInternalID)
	} else {
		// Generate note with placeholder
		note = "========================================\n" +
			"INSTALL LOGS COLLECTION INSTRUCTIONS\n" +
			"========================================\n\n" +
			"For cluster provisioning failures, the cluster may not be accessible via 'oc' commands.\n" +
			"In such cases, you can collect install logs directly from OCM.\n\n" +
			"COMMAND TO COLLECT INSTALL LOGS:\n" +
			"---------------------------------\n\n" +
			"echo -e `ocm get /api/clusters_mgmt/v1/clusters/${INTERNAL_ID}/resources | jq -r '.resources.install_logs_tail'` > install-logs.txt\n\n" +
			"STEPS:\n" +
			"------\n\n" +
			"1. Find your cluster's INTERNAL_ID:\n" +
			"   ocm list clusters\n\n" +
			"   Example output:\n" +
			"   ID                   NAME            STATE\n" +
			"   abc123def456...      my-cluster      installing\n\n" +
			"2. Export the INTERNAL_ID:\n" +
			"   export INTERNAL_ID=abc123def456...\n\n" +
			"3. Collect the install logs:\n" +
			"   echo -e `ocm get /api/clusters_mgmt/v1/clusters/${INTERNAL_ID}/resources | jq -r '.resources.install_logs_tail'` > install-logs.txt\n\n" +
			"4. Review the logs for error messages:\n" +
			"   less install-logs.txt\n\n" +
			"   Look for:\n" +
			"   - ERROR level messages\n" +
			"   - Failed operations\n" +
			"   - Timeout errors\n" +
			"   - Permission/authorization errors\n" +
			"   - Resource provisioning failures\n" +
			"   - Network connectivity issues\n\n" +
			"COMMON INSTALL FAILURE PATTERNS:\n" +
			"---------------------------------\n\n" +
			"1. AWS: IAM permission issues, instance limits, EBS volume limits\n" +
			"2. Azure: Subscription limits, resource provider not registered\n" +
			"3. GCP: API not enabled, service account permissions\n" +
			"4. Network: VPC/subnet configuration, DNS resolution failures\n" +
			"5. Resources: Insufficient quota, unavailable instance types\n\n" +
			"ADDITIONAL OCM COMMANDS:\n" +
			"------------------------\n\n" +
			"# Get cluster status\n" +
			"ocm get cluster ${INTERNAL_ID}\n\n" +
			"# Get cluster details (JSON)\n" +
			"ocm get cluster ${INTERNAL_ID} --json\n\n" +
			"# Get cluster events\n" +
			"ocm get /api/clusters_mgmt/v1/clusters/${INTERNAL_ID}/events\n\n" +
			"# Get cluster installation status\n" +
			"ocm get /api/clusters_mgmt/v1/clusters/${INTERNAL_ID} | jq '.status'\n\n" +
			"TIP: You can re-run this command with --cluster-id flag:\n" +
			"     osdctl assist cluster-provisioning-failure --cluster-id <INTERNAL_ID>\n\n" +
			"========================================\n"
	}

	if err := o.writeFile(noteFile, note); err != nil {
		return fmt.Errorf("failed to write install logs note: %w", err)
	}

	safeColorPrintf(green, "  ✓ Saved to 00-INSTALL-LOGS-COLLECTION.txt\n\n")
	return nil
}

func (o *clusterProvisioningFailureOptions) collectInstallLogs(yellow, green, red *color.Color) error {
	if o.clusterInternalID == "" {
		safeColorPrintln(yellow, "Skipping install logs collection: Internal ID not available")
		return fmt.Errorf("internal ID not available")
	}

	safeColorPrintf(yellow, "Collecting: Install logs via OCM for cluster %s\n", o.clusterID)

	// Run: echo -e `ocm get /api/clusters_mgmt/v1/clusters/<INTERNAL_ID>/resources | jq -r '.resources.install_logs_tail'`
	cmd := exec.Command("bash", "-c",
		fmt.Sprintf("ocm get /api/clusters_mgmt/v1/clusters/%s/resources | jq -r '.resources.install_logs_tail'", o.clusterInternalID))

	output, err := cmd.CombinedOutput()
	if err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect install logs via OCM: %v\n", err)
		safeColorPrintf(red, "    Output: %s\n\n", string(output))
		return fmt.Errorf("failed to run OCM command: %w", err)
	}

	// Save the install logs
	installLogsFile := filepath.Join(o.outputDir, "01-install-logs.txt")
	if err := o.writeFile(installLogsFile, string(output)); err != nil {
		safeColorPrintf(red, "  ✗ Failed to save install logs\n\n")
		return err
	}

	safeColorPrintf(green, "  ✓ Saved to 01-install-logs.txt\n\n")
	return nil
}

func (o *clusterProvisioningFailureOptions) collectClusterInfoViaOCM(yellow, green, red *color.Color) error {
	safeColorPrintf(yellow, "Collecting: Cluster information via OCM for cluster %s\n", o.clusterID)

	// Get cluster description
	cmd := exec.Command("ocm", "describe", "cluster", o.clusterID)
	output, err := cmd.CombinedOutput()
	if err != nil {
		safeColorPrintf(red, "  ✗ Failed to get cluster description via OCM: %v\n\n", err)
		return fmt.Errorf("failed to run ocm describe: %w", err)
	}

	clusterInfoFile := filepath.Join(o.outputDir, "02-cluster-info-ocm.txt")
	if err := o.writeFile(clusterInfoFile, string(output)); err != nil {
		safeColorPrintf(red, "  ✗ Failed to save cluster info\n\n")
		return err
	}
	safeColorPrintf(green, "  ✓ Saved to 02-cluster-info-ocm.txt\n\n")

	// Get cluster status (JSON)
	safeColorPrintln(yellow, "Collecting: Cluster details (JSON)")
	cmd = exec.Command("ocm", "describe", "cluster", o.clusterID, "--json")
	output, err = cmd.CombinedOutput()
	if err != nil {
		safeColorPrintf(red, "  ✗ Failed to get cluster JSON via OCM: %v\n\n", err)
	} else {
		clusterJSONFile := filepath.Join(o.outputDir, "02-cluster-info-ocm.json")
		if err := o.writeFile(clusterJSONFile, string(output)); err != nil {
			safeColorPrintf(red, "  ✗ Failed to save cluster JSON\n\n")
		} else {
			safeColorPrintf(green, "  ✓ Saved to 02-cluster-info-ocm.json\n\n")
		}
	}

	// Get cluster events if internal ID is available
	if o.clusterInternalID != "" {
		safeColorPrintln(yellow, "Collecting: Cluster events via OCM")
		cmd = exec.Command("bash", "-c",
			fmt.Sprintf("ocm get /api/clusters_mgmt/v1/clusters/%s/events", o.clusterInternalID))
		output, err = cmd.CombinedOutput()
		if err != nil {
			safeColorPrintf(red, "  ✗ Failed to get cluster events via OCM: %v\n\n", err)
		} else {
			eventsFile := filepath.Join(o.outputDir, "03-cluster-events-ocm.txt")
			if err := o.writeFile(eventsFile, string(output)); err != nil {
				safeColorPrintf(red, "  ✗ Failed to save cluster events\n\n")
			} else {
				safeColorPrintf(green, "  ✓ Saved to 03-cluster-events-ocm.txt\n\n")
			}
		}
	}

	return nil
}

func (o *clusterProvisioningFailureOptions) generateSummaryForFailedInstall(green *color.Color) error {
	safeColorPrintln(green, "Generating summary report...")

	summaryFile := filepath.Join(o.outputDir, "00-SUMMARY.txt")
	summary := fmt.Sprintf(`ClusterProvisioningFailure Diagnostic Collection Summary
====================================================
Collection Date: %s
Cluster ID: %s
Internal ID: %s

This collection focuses on install logs for a failed cluster installation.
The cluster is not accessible via 'oc' commands, so all information is gathered via OCM.

Files Collected:
----------------
`, time.Now().Format(time.RFC3339), o.clusterID, o.clusterInternalID)

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

	// Add install logs preview if available
	installLogsFile := filepath.Join(o.outputDir, "01-install-logs.txt")
	if content, err := os.ReadFile(installLogsFile); err == nil {
		summary += "\nInstall Logs Preview (last 100 lines):\n"
		lines := strings.Split(string(content), "\n")
		startIdx := len(lines) - 100
		if startIdx < 0 {
			startIdx = 0
		}
		preview := strings.Join(lines[startIdx:], "\n")
		if len(preview) > 5000 {
			preview = preview[:5000] + "\n... (truncated)"
		}
		summary += preview + "\n"
	} else {
		summary += "\nInstall Logs: Not available - see 00-INSTALL-LOGS-COLLECTION.txt for manual collection instructions\n"
	}

	// Add cluster info if available
	clusterInfoFile := filepath.Join(o.outputDir, "02-cluster-info-ocm.txt")
	if content, err := os.ReadFile(clusterInfoFile); err == nil {
		summary += "\nCluster Information:\n"
		summary += string(content) + "\n"
	}

	summary += "\nNext Steps:\n-----------\n"
	summary += "1. Review install logs in 01-install-logs.txt for ERROR messages\n"
	summary += "2. Look for common failure patterns:\n"
	summary += "   - IAM/permission errors\n"
	summary += "   - Resource quota/limit errors\n"
	summary += "   - Network/connectivity errors\n"
	summary += "   - Timeout errors\n"
	summary += "3. Check cluster info in 02-cluster-info-ocm.txt for cluster state\n"
	summary += "4. Refer to https://github.com/openshift/ops-sop/blob/master/v4/alerts/ClusterProvisioningFailure.md\n"

	return o.writeFile(summaryFile, summary)
}

func (o *clusterProvisioningFailureOptions) collectPodsInCriticalNamespaces(yellow, green, red *color.Color) error {
	// Collect pod status from critical namespaces
	namespaces := []string{
		"openshift-cluster-version",
		"openshift-machine-api",
		"openshift-kube-apiserver",
		"openshift-etcd",
		"openshift-authentication",
		"openshift-ingress",
	}

	for _, ns := range namespaces {
		safeColorPrintf(yellow, "Collecting: Pods in namespace %s\n", ns)
		podsFile := filepath.Join(o.outputDir, fmt.Sprintf("12-pods-%s.txt", ns))
		if err := o.runOCCommand("get", []string{"pods", "-n", ns, "-o", "wide"}, podsFile); err != nil {
			safeColorPrintf(red, "  ✗ Failed to collect pods in %s\n\n", ns)
		} else {
			safeColorPrintf(green, "  ✓ Saved to 12-pods-%s.txt\n\n", ns)
		}
	}

	return nil
}

func (o *clusterProvisioningFailureOptions) runOCCommand(subcommand string, args []string, outputFile string) error {
	return o.commandExecutor(subcommand, args, outputFile)
}

func (o *clusterProvisioningFailureOptions) runOCCommandWithOutput(subcommand string, args []string) (string, error) {
	return o.commandRunner(subcommand, args)
}

func (o *clusterProvisioningFailureOptions) writeFile(filePath string, content string) error {
	return os.WriteFile(filePath, []byte(content), 0644)
}

func (o *clusterProvisioningFailureOptions) generateSummary(clusterID string, green *color.Color) error {
	safeColorPrintln(green, "Generating summary report...")

	summaryFile := filepath.Join(o.outputDir, "00-SUMMARY.txt")
	summary := fmt.Sprintf(`ClusterProvisioningFailure Diagnostic Collection Summary
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

	// Add cluster version status
	cvFile := filepath.Join(o.outputDir, "01-clusterversion.txt")
	if content, err := os.ReadFile(cvFile); err == nil {
		summary += "\nCluster Version:\n"
		summary += string(content) + "\n"
	}

	// Add cluster operators status
	coFile := filepath.Join(o.outputDir, "02-clusteroperators.txt")
	if content, err := os.ReadFile(coFile); err == nil {
		summary += "\nCluster Operators Status:\n"
		summary += string(content) + "\n"
	}

	// Add nodes status
	nodesFile := filepath.Join(o.outputDir, "06-nodes.txt")
	if content, err := os.ReadFile(nodesFile); err == nil {
		summary += "\nNodes Status:\n"
		summary += string(content) + "\n"
	}

	// Add machines status
	machinesFile := filepath.Join(o.outputDir, "05-machines.txt")
	if content, err := os.ReadFile(machinesFile); err == nil {
		summary += "\nMachines Status:\n"
		summary += string(content) + "\n"
	}

	return o.writeFile(summaryFile, summary)
}

func (o *clusterProvisioningFailureOptions) createTarball() error {
	cmd := exec.Command("tar", "-czf", o.outputDir+".tar.gz", o.outputDir)
	return cmd.Run()
}

// extractAndReadDiagnostics extracts key diagnostic files and returns their content
func (o *clusterProvisioningFailureOptions) extractAndReadDiagnostics() (string, error) {
	var content strings.Builder

	// Priority files to read (focused on install logs for provisioning failures)
	priorityFiles := []string{
		"00-SUMMARY.txt",
		"01-install-logs.txt",
		"02-cluster-info-ocm.txt",
		"03-cluster-events-ocm.txt",
	}

	// Read priority files first
	for _, filename := range priorityFiles {
		filePath := filepath.Join(o.outputDir, filename)
		if data, err := os.ReadFile(filePath); err == nil {
			content.WriteString(fmt.Sprintf("\n=== %s ===\n", filename))

			// Limit install logs size to avoid token limits
			fileContent := string(data)
			if filename == "01-install-logs.txt" && len(fileContent) > 20000 {
				// For install logs, take both beginning and end
				beginning := fileContent[:10000]
				end := fileContent[len(fileContent)-10000:]
				fileContent = beginning + "\n\n... (middle section truncated) ...\n\n" + end
			} else if len(fileContent) > 15000 {
				fileContent = fileContent[:15000] + "\n... (truncated)"
			}

			content.WriteString(fileContent)
			content.WriteString("\n")
		}
	}

	// Read JSON cluster info if available
	jsonFile := filepath.Join(o.outputDir, "02-cluster-info-ocm.json")
	if data, err := os.ReadFile(jsonFile); err == nil {
		content.WriteString(fmt.Sprintf("\n=== %s ===\n", filepath.Base(jsonFile)))
		jsonContent := string(data)
		if len(jsonContent) > 10000 {
			jsonContent = jsonContent[:10000] + "\n... (truncated)"
		}
		content.WriteString(jsonContent)
		content.WriteString("\n")
	}

	return content.String(), nil
}

// analyzeWithLLM sends diagnostic content to LLM for analysis and returns the response and conversation history
func (o *clusterProvisioningFailureOptions) analyzeWithLLM(diagnosticContent string) (string, []Message, error) {
	systemPrompt := clusterProvisioningSystemPromptTemplate
	// Fallback to default if embed failed (shouldn't happen, but be safe)
	if systemPrompt == "" {
		systemPrompt = `You are an expert OpenShift/Kubernetes Site Reliability Engineer (SRE) specializing in cluster installation and provisioning. Your task is to analyze diagnostic information and assess cluster provisioning failures on OpenShift clusters.

Analyze the provided diagnostic data and provide:
1. Root cause analysis - What is likely causing the cluster provisioning failure?
2. Key findings - What are the most important issues identified?
3. Recommended actions - What steps should be taken to resolve the issues?
4. Priority - Rate the severity (Critical/High/Medium/Low)

Focus on:
- ClusterOperators that are degraded, unavailable, or progressing
- Node provisioning issues
- Machine API problems
- Infrastructure configuration issues
- Installation progress and any stuck components

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
			Content: fmt.Sprintf("Please analyze the following cluster provisioning failure diagnostic information from an OpenShift cluster:\n\n%s", diagnosticContent),
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
func (o *clusterProvisioningFailureOptions) askFollowUpQuestion(conversationHistory []Message, question string) (string, []Message, error) {
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
func (o *clusterProvisioningFailureOptions) interactiveFollowUp(conversationHistory []Message, green, yellow *color.Color) error {
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
		analysisFile := filepath.Join(o.outputDir, "10-llm-analysis.txt")
		appendContent := fmt.Sprintf("\n\n=== Follow-up Question ===\n%s\n\n=== Response ===\n%s\n", question, response)
		if err := o.appendToFile(analysisFile, appendContent); err != nil {
			fmt.Printf("Warning: Failed to append follow-up to analysis file: %v\n", err)
		}
	}

	return nil
}

// appendToFile appends content to a file
func (o *clusterProvisioningFailureOptions) appendToFile(filePath string, content string) error {
	file, err := os.OpenFile(filePath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.WriteString(content)
	return err
}
