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
	"github.com/spf13/viper"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
)

//go:embed prompts/pruning_cronjob_analysis.md
var systemPromptTemplate string

type pruningCronjobOptions struct {
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

// Helper functions for safe color printing (handles nil colors for testing)
func safeColorPrintln(c *color.Color, msg string) {
	if c != nil {
		c.Println(msg)
	} else {
		fmt.Println(msg)
	}
}

func safeColorPrintf(c *color.Color, format string, args ...interface{}) {
	if c != nil {
		c.Printf(format, args...)
	} else {
		fmt.Printf(format, args...)
	}
}

// loadConfigValue reads a value from ~/.config/osdctl config file, then falls back to environment variable
func loadConfigValue(configKey, envVarName, defaultValue string) string {
	// Try to read from config file first
	homeDir, err := os.UserHomeDir()
	if err == nil {
		configPath := filepath.Join(homeDir, ".config", "osdctl")
		if _, err := os.Stat(configPath); err == nil {
			// Create a new viper instance to avoid conflicts
			v := viper.New()
			v.SetConfigFile(configPath)
			v.SetConfigType("yaml")
			if err := v.ReadInConfig(); err == nil {
				if value := v.GetString(configKey); value != "" {
					// Remove quotes if present (YAML may have quoted strings)
					value = strings.Trim(value, "\"'")
					return strings.TrimSpace(value)
				}
			}
		}
	}
	
	// Fall back to environment variable if envVarName is provided
	if envVarName != "" {
		if envValue := os.Getenv(envVarName); envValue != "" {
			return strings.TrimSpace(envValue)
		}
	}
	
	// Return default if provided
	return defaultValue
}

// NewCmdPruningCronjobErrorSRE implements the pruning-cronjob-error-sre command
func NewCmdPruningCronjobErrorSRE() *cobra.Command {
	ops := newPruningCronjobOptions()
	cmd := &cobra.Command{
		Use:   "pruning-cronjob-error-sre",
		Short: "Collect diagnostic information for PruningCronjobErrorSRE alert",
		Long: `Collects all diagnostic information needed to troubleshoot the PruningCronjobErrorSRE alert.

This command gathers comprehensive diagnostic data including:
  - Job and pod status in the openshift-sre-pruning namespace
  - Pod logs and describe output for failing pods
  - Events and resource quotas
  - Network configuration (SDN vs OVN)
  - node-exporter CPU usage
  - Image registry status and logs
  - OVN master pod status
  - CronJob information
  - Seccomp error detection
  - Cluster version information

All diagnostic files are saved to a timestamped directory and a summary report
is generated. The output can optionally be archived as a tarball.

The command requires:
  - OpenShift CLI (oc) to be installed and available in PATH
  - Active cluster connection (via 'ocm backplane login')

For troubleshooting steps, refer to:
  ~/ops-sop/v4/alerts/PruningCronjobErrorSRE.md
`,
		Example: `  # Collect diagnostics with default output directory
  osdctl assist pruning-cronjob-error-sre

  # Collect diagnostics to a custom directory
  osdctl assist pruning-cronjob-error-sre --output-dir /tmp/my-diagnostics

  # Collect diagnostics and analyze with LLM
  osdctl assist pruning-cronjob-error-sre --analyze

  # Analyze existing directory of diagnostic artifacts
  osdctl assist pruning-cronjob-error-sre --analyze-existing /path/to/existing-diagnostics

  # Collect diagnostics with custom LLM configuration
  osdctl assist pruning-cronjob-error-sre --analyze --llm-model gpt-4o --llm-base-url https://api.openai.com/v1`,
		Args:              cobra.NoArgs,
		DisableAutoGenTag: true,
		Run: func(cmd *cobra.Command, args []string) {
			cmdutil.CheckErr(ops.complete(cmd, args))
			cmdutil.CheckErr(ops.run())
		},
	}

	cmd.Flags().StringVar(&ops.outputDir, "output-dir", "", "Output directory for diagnostic files (default: pruning-cronjob-diagnostics-TIMESTAMP)")
	cmd.Flags().StringVar(&ops.existingDir, "analyze-existing", "", "Path to existing directory of diagnostic artifacts to analyze with LLM (skips collection)")
	cmd.Flags().BoolVar(&ops.enableLLMAnalysis, "analyze", false, "Enable LLM analysis of collected diagnostic files")
	cmd.Flags().StringVar(&ops.llmAPIKey, "llm-api-key", "", "LLM API key (default: checks ~/.config/osdctl OPENAI_API_KEY, then env vars: LLM_API_KEY, OPENAI_API_KEY, etc.)")
	cmd.Flags().StringVar(&ops.llmBaseURL, "llm-base-url", "", "LLM API base URL (default: checks ~/.config/osdctl OPENAI_BASE_URL, then env vars, or https://api.openai.com/v1)")
	cmd.Flags().StringVar(&ops.llmModel, "llm-model", "gpt-4o-mini", "LLM model to use for analysis (default: checks ~/.config/osdctl AI_MODEL_NAME, then env vars, or gpt-4o-mini)")
	return cmd
}

func newPruningCronjobOptions() *pruningCronjobOptions {
	return &pruningCronjobOptions{
		commandExecutor: defaultCommandExecutor,
		commandRunner:   defaultCommandRunner,
	}
}

func (o *pruningCronjobOptions) complete(cmd *cobra.Command, _ []string) error {
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
		o.outputDir = fmt.Sprintf("pruning-cronjob-diagnostics-%s", timestamp)
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

// defaultCommandExecutor executes oc commands and writes output to file
func defaultCommandExecutor(name string, args []string, outputFile string) error {
	cmdArgs := []string{name}
	cmdArgs = append(cmdArgs, args...)
	
	cmd := exec.Command("oc", cmdArgs...)
	
	if outputFile == "/dev/null" {
		cmd.Stdout = nil
		cmd.Stderr = nil
		return cmd.Run()
	}

	file, err := os.OpenFile(outputFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	cmd.Stdout = file
	cmd.Stderr = file
	return cmd.Run()
}

// defaultCommandRunner executes oc commands and returns output
func defaultCommandRunner(name string, args []string) (string, error) {
	cmdArgs := []string{name}
	cmdArgs = append(cmdArgs, args...)
	
	cmd := exec.Command("oc", cmdArgs...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func (o *pruningCronjobOptions) run() error {
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
		green.Println("Collecting diagnostic information for PruningCronjobErrorSRE alert...")
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
		if err := o.collectJobs(yellow, green, red); err != nil {
			return err
		}
		if err := o.collectPods(yellow, green, red); err != nil {
			return err
		}
		if err := o.collectEvents(yellow, green, red); err != nil {
			return err
		}
		if err := o.collectNetworkInfo(yellow, green, red); err != nil {
			return err
		}
		if err := o.collectNodeExporterInfo(yellow, green, red); err != nil {
			return err
		}
		if err := o.collectImageRegistryInfo(yellow, green, red); err != nil {
			return err
		}
		if err := o.collectResourceQuotas(yellow, green, red); err != nil {
			return err
		}
		if err := o.collectOVNInfo(yellow, green, red); err != nil {
			return err
		}
		if err := o.collectCronJobInfo(yellow, green, red); err != nil {
			return err
		}
		if err := o.collectSeccompErrors(yellow, green, red); err != nil {
			return err
		}
		if err := o.collectNodeInfo(yellow, green, red); err != nil {
			return err
		}
		if err := o.collectJobHistory(yellow, green, red); err != nil {
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
				analysisFile := filepath.Join(o.outputDir, "18-llm-analysis.txt")
				if err := o.writeFile(analysisFile, analysis); err != nil {
					fmt.Printf("Warning: Failed to save LLM analysis: %v\n", err)
				} else {
					green.Printf("✓ LLM analysis saved to 18-llm-analysis.txt\n")
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
		fmt.Println("3. Review 18-llm-analysis.txt for AI-powered insights")
		fmt.Println("4. Check pod logs and describe output for error details")
		fmt.Println("5. Refer to ~/ops-sop/v4/alerts/PruningCronjobErrorSRE.md for troubleshooting steps")
	} else {
		fmt.Println("3. Check pod logs and describe output for error details")
		fmt.Println("4. Refer to ~/ops-sop/v4/alerts/PruningCronjobErrorSRE.md for troubleshooting steps")
		fmt.Println("5. Use --analyze flag to enable LLM analysis")
	}
	fmt.Println()

	return nil
}

func (o *pruningCronjobOptions) runOCCommand(subcommand string, args []string, outputFile string) error {
	return o.commandExecutor(subcommand, args, outputFile)
}

func (o *pruningCronjobOptions) runOCCommandWithOutput(subcommand string, args []string) (string, error) {
	return o.commandRunner(subcommand, args)
}

func (o *pruningCronjobOptions) getClusterID() (string, error) {
	output, err := o.runOCCommandWithOutput("get", []string{"clusterversion", "version", "-o", "jsonpath={.spec.clusterID}"})
	if err != nil {
		return "N/A", nil
	}
	clusterID := strings.TrimSpace(output)
	if clusterID == "" {
		return "N/A", nil
	}
	return clusterID, nil
}

func (o *pruningCronjobOptions) writeFile(filename, content string) error {
	return os.WriteFile(filename, []byte(content), 0644)
}

func (o *pruningCronjobOptions) collectJobs(yellow, green, red *color.Color) error {
	yellow.Println("Collecting: Jobs in openshift-sre-pruning namespace")
	if err := o.runOCCommand("get", []string{"job", "-n", "openshift-sre-pruning", "-o", "wide"}, filepath.Join(o.outputDir, "01-jobs.txt")); err != nil {
		red.Printf("  ✗ Failed to collect jobs\n\n")
	} else {
		green.Printf("  ✓ Saved to 01-jobs.txt\n\n")
	}

	yellow.Println("Collecting: Jobs in openshift-sre-pruning namespace (yaml)")
	if err := o.runOCCommand("get", []string{"job", "-n", "openshift-sre-pruning", "-o", "yaml"}, filepath.Join(o.outputDir, "01-jobs.yaml")); err != nil {
		red.Printf("  ✗ Failed to collect jobs yaml\n\n")
	} else {
		green.Printf("  ✓ Saved to 01-jobs.yaml\n\n")
	}
	return nil
}

func (o *pruningCronjobOptions) collectPods(yellow, green, red *color.Color) error {
	safeColorPrintln(yellow, "Collecting: Pods in openshift-sre-pruning namespace")
	if err := o.runOCCommand("get", []string{"pod", "-n", "openshift-sre-pruning", "-o", "wide"}, filepath.Join(o.outputDir, "02-pods.txt")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect pods\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 02-pods.txt\n\n")
	}

	safeColorPrintln(yellow, "Collecting: Pods in openshift-sre-pruning namespace (yaml)")
	if err := o.runOCCommand("get", []string{"pod", "-n", "openshift-sre-pruning", "-o", "yaml"}, filepath.Join(o.outputDir, "02-pods.yaml")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect pods yaml\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 02-pods.yaml\n\n")
	}

	// Get failing pods
	output, _ := o.runOCCommandWithOutput("get", []string{"pod", "-n", "openshift-sre-pruning", "-o", "json"})
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
	var allPods []string
	if err := json.Unmarshal([]byte(output), &podList); err == nil {
		for _, pod := range podList.Items {
			allPods = append(allPods, pod.Metadata.Name)
			if pod.Status.Phase != "Succeeded" && pod.Status.Phase != "Running" {
				failingPods = append(failingPods, pod.Metadata.Name)
			}
		}
	}

	podsToProcess := failingPods
	if len(failingPods) == 0 {
		safeColorPrintln(yellow, "No failing pods found, collecting logs for all pods...")
		fmt.Println()
		podsToProcess = allPods
	} else {
		safeColorPrintln(yellow, "Found failing pods, collecting detailed information...")
		fmt.Println()
	}

	for _, pod := range podsToProcess {
		safeColorPrintf(yellow, "Collecting: Logs for pod %s\n", pod)
		logFile := filepath.Join(o.outputDir, fmt.Sprintf("03-pod-logs-%s.txt", pod))
		if err := o.runOCCommand("logs", []string{pod, "-n", "openshift-sre-pruning", "--all-containers=true"}, logFile); err != nil {
			safeColorPrintf(red, "  ✗ Failed to collect logs for pod %s\n\n", pod)
		} else {
			safeColorPrintf(green, "  ✓ Saved to 03-pod-logs-%s.txt\n\n", pod)
		}

		safeColorPrintf(yellow, "Collecting: Describe output for pod %s\n", pod)
		describeFile := filepath.Join(o.outputDir, fmt.Sprintf("04-pod-describe-%s.txt", pod))
		if err := o.runOCCommand("describe", []string{"pod", pod, "-n", "openshift-sre-pruning"}, describeFile); err != nil {
			safeColorPrintf(red, "  ✗ Failed to collect describe for pod %s\n\n", pod)
		} else {
			safeColorPrintf(green, "  ✓ Saved to 04-pod-describe-%s.txt\n\n", pod)
		}
	}

	// Collect job describe for all jobs
	jobOutput, _ := o.runOCCommandWithOutput("get", []string{"job", "-n", "openshift-sre-pruning", "-o", "json"})
	var jobList struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}

	if err := json.Unmarshal([]byte(jobOutput), &jobList); err == nil {
		for _, job := range jobList.Items {
			safeColorPrintf(yellow, "Collecting: Describe output for job %s\n", job.Metadata.Name)
			describeFile := filepath.Join(o.outputDir, fmt.Sprintf("12-job-describe-%s.txt", job.Metadata.Name))
			if err := o.runOCCommand("describe", []string{"job", job.Metadata.Name, "-n", "openshift-sre-pruning"}, describeFile); err != nil {
				safeColorPrintf(red, "  ✗ Failed to collect describe for job %s\n\n", job.Metadata.Name)
			} else {
				safeColorPrintf(green, "  ✓ Saved to 12-job-describe-%s.txt\n\n", job.Metadata.Name)
			}
		}
	}

	return nil
}

func (o *pruningCronjobOptions) collectEvents(yellow, green, red *color.Color) error {
	yellow.Println("Collecting: Events in openshift-sre-pruning namespace")
	if err := o.runOCCommand("get", []string{"events", "-n", "openshift-sre-pruning", "--sort-by=.lastTimestamp"}, filepath.Join(o.outputDir, "05-events.txt")); err != nil {
		red.Printf("  ✗ Failed to collect events\n\n")
	} else {
		green.Printf("  ✓ Saved to 05-events.txt\n\n")
	}
	return nil
}

func (o *pruningCronjobOptions) collectNetworkInfo(yellow, green, red *color.Color) error {
	yellow.Println("Collecting: Network type configuration")
	if err := o.runOCCommand("get", []string{"Network.config.openshift.io", "cluster", "-o", "json"}, filepath.Join(o.outputDir, "06-network-config.json")); err != nil {
		red.Printf("  ✗ Failed to collect network config\n\n")
	} else {
		green.Printf("  ✓ Saved to 06-network-config.json\n\n")
	}
	return nil
}

func (o *pruningCronjobOptions) collectNodeExporterInfo(yellow, green, red *color.Color) error {
	safeColorPrintln(yellow, "Collecting: node-exporter pod CPU usage")
	output, _ := o.runOCCommandWithOutput("adm", []string{"top", "pod", "-n", "openshift-monitoring"})
	lines := strings.Split(output, "\n")
	var nodeExporterLines []string
	for _, line := range lines {
		if strings.Contains(line, "node-exporter") {
			nodeExporterLines = append(nodeExporterLines, line)
		}
	}
	content := strings.Join(nodeExporterLines, "\n")
	if content == "" {
		content = "No node-exporter pods found"
	}
	if err := o.writeFile(filepath.Join(o.outputDir, "07-node-exporter-cpu.txt"), content); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect node-exporter CPU\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 07-node-exporter-cpu.txt\n\n")
	}

	safeColorPrintln(yellow, "Collecting: node-exporter pods with node information")
	output, _ = o.runOCCommandWithOutput("get", []string{"pod", "-n", "openshift-monitoring", "-o", "wide"})
	lines = strings.Split(output, "\n")
	nodeExporterLines = []string{}
	for _, line := range lines {
		if strings.Contains(line, "node-exporter") {
			nodeExporterLines = append(nodeExporterLines, line)
		}
	}
	content = strings.Join(nodeExporterLines, "\n")
	if content == "" {
		content = "No node-exporter pods found"
	}
	if err := o.writeFile(filepath.Join(o.outputDir, "07-node-exporter-pods.txt"), content); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect node-exporter pods\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 07-node-exporter-pods.txt\n\n")
	}
	return nil
}

func (o *pruningCronjobOptions) collectImageRegistryInfo(yellow, green, red *color.Color) error {
	safeColorPrintln(yellow, "Collecting: Image registry pods status")
	if err := o.runOCCommand("get", []string{"pod", "-n", "openshift-image-registry", "-o", "wide"}, filepath.Join(o.outputDir, "08-image-registry-pods.txt")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect image registry pods\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 08-image-registry-pods.txt\n\n")
	}

	safeColorPrintln(yellow, "Collecting: cluster-image-registry-operator logs (forbidden errors)")
	output, _ := o.runOCCommandWithOutput("logs", []string{"-n", "openshift-image-registry", "-l", "name=cluster-image-registry-operator", "--tail=1000"})
	lines := strings.Split(output, "\n")
	var forbiddenLines []string
	for _, line := range lines {
		if strings.Contains(strings.ToLower(line), "forbidden") {
			forbiddenLines = append(forbiddenLines, line)
		}
	}
	content := strings.Join(forbiddenLines, "\n")
	if content == "" {
		content = "No forbidden errors found"
	}
	if err := o.writeFile(filepath.Join(o.outputDir, "09-registry-operator-forbidden.txt"), content); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect forbidden errors\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 09-registry-operator-forbidden.txt\n\n")
	}

	safeColorPrintln(yellow, "Collecting: cluster-image-registry-operator full logs")
	if err := o.runOCCommand("logs", []string{"-n", "openshift-image-registry", "-l", "name=cluster-image-registry-operator", "--tail=500"}, filepath.Join(o.outputDir, "09-registry-operator-logs.txt")); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect registry operator logs\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 09-registry-operator-logs.txt\n\n")
	}
	return nil
}

func (o *pruningCronjobOptions) collectResourceQuotas(yellow, green, red *color.Color) error {
	yellow.Println("Collecting: Resource quotas in openshift-monitoring")
	if err := o.runOCCommand("get", []string{"resourcequota", "-n", "openshift-monitoring"}, filepath.Join(o.outputDir, "10-resource-quotas-monitoring.txt")); err != nil {
		red.Printf("  ✗ Failed to collect resource quotas\n\n")
	} else {
		green.Printf("  ✓ Saved to 10-resource-quotas-monitoring.txt\n\n")
	}

	yellow.Println("Collecting: Resource quotas in openshift-sre-pruning")
	if err := o.runOCCommand("get", []string{"resourcequota", "-n", "openshift-sre-pruning"}, filepath.Join(o.outputDir, "10-resource-quotas-pruning.txt")); err != nil {
		red.Printf("  ✗ Failed to collect resource quotas\n\n")
	} else {
		green.Printf("  ✓ Saved to 10-resource-quotas-pruning.txt\n\n")
	}
	return nil
}

func (o *pruningCronjobOptions) collectOVNInfo(yellow, green, red *color.Color) error {
	yellow.Println("Collecting: OVN master pods status")
	if err := o.runOCCommand("get", []string{"pod", "-n", "openshift-ovn-kubernetes", "-l", "app=ovnkube-master", "-o", "wide"}, filepath.Join(o.outputDir, "11-ovn-master-pods.txt")); err != nil {
		red.Printf("  ✗ Failed to collect OVN master pods\n\n")
	} else {
		green.Printf("  ✓ Saved to 11-ovn-master-pods.txt\n\n")
	}
	return nil
}

func (o *pruningCronjobOptions) collectCronJobInfo(yellow, green, red *color.Color) error {
	yellow.Println("Collecting: CronJobs in openshift-sre-pruning namespace")
	if err := o.runOCCommand("get", []string{"cronjob", "-n", "openshift-sre-pruning", "-o", "wide"}, filepath.Join(o.outputDir, "13-cronjobs.txt")); err != nil {
		red.Printf("  ✗ Failed to collect cronjobs\n\n")
	} else {
		green.Printf("  ✓ Saved to 13-cronjobs.txt\n\n")
	}

	yellow.Println("Collecting: CronJobs in openshift-sre-pruning namespace (yaml)")
	if err := o.runOCCommand("get", []string{"cronjob", "-n", "openshift-sre-pruning", "-o", "yaml"}, filepath.Join(o.outputDir, "13-cronjobs.yaml")); err != nil {
		red.Printf("  ✗ Failed to collect cronjobs yaml\n\n")
	} else {
		green.Printf("  ✓ Saved to 13-cronjobs.yaml\n\n")
	}
	return nil
}

func (o *pruningCronjobOptions) collectSeccompErrors(yellow, green, red *color.Color) error {
	safeColorPrintln(yellow, "Collecting: Checking for seccomp errors in pod descriptions")
	output, _ := o.runOCCommandWithOutput("get", []string{"pod", "-n", "openshift-sre-pruning", "-o", "json"})
	
	var podList struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				ContainerStatuses []struct {
					State struct {
						Waiting struct {
							Reason string `json:"reason"`
						} `json:"waiting"`
						Terminated struct {
							Reason string `json:"reason"`
						} `json:"terminated"`
					} `json:"state"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}

	var seccompPods []string
	if err := json.Unmarshal([]byte(output), &podList); err == nil {
		for _, pod := range podList.Items {
			for _, containerStatus := range pod.Status.ContainerStatuses {
				reason := containerStatus.State.Waiting.Reason + containerStatus.State.Terminated.Reason
				if strings.Contains(strings.ToLower(reason), "seccomp") {
					seccompPods = append(seccompPods, pod.Metadata.Name)
					break
				}
			}
		}
	}

	content := strings.Join(seccompPods, "\n")
	if content == "" {
		content = "No seccomp errors detected"
	}
	if err := o.writeFile(filepath.Join(o.outputDir, "14-seccomp-errors.txt"), content); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect seccomp errors\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 14-seccomp-errors.txt\n\n")
	}
	return nil
}

func (o *pruningCronjobOptions) collectNodeInfo(yellow, green, red *color.Color) error {
	yellow.Println("Collecting: Node information for pruning pods")
	if err := o.runOCCommand("get", []string{"pod", "-n", "openshift-sre-pruning", "-o", "jsonpath={range .items[*]}{.metadata.name}{\"\\t\"}{.spec.nodeName}{\"\\n\"}{end}"}, filepath.Join(o.outputDir, "15-pod-nodes.txt")); err != nil {
		red.Printf("  ✗ Failed to collect node information\n\n")
	} else {
		green.Printf("  ✓ Saved to 15-pod-nodes.txt\n\n")
	}
	return nil
}

func (o *pruningCronjobOptions) collectJobHistory(yellow, green, red *color.Color) error {
	safeColorPrintln(yellow, "Collecting: Recent job history")
	output, _ := o.runOCCommandWithOutput("get", []string{"job", "-n", "openshift-sre-pruning", "-o", "json"})
	
	var jobList struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Failed    *int32 `json:"failed"`
				Succeeded *int32 `json:"succeeded"`
			} `json:"status"`
		} `json:"items"`
	}

	var historyLines []string
	if err := json.Unmarshal([]byte(output), &jobList); err == nil {
		for _, job := range jobList.Items {
			failed := int32(0)
			succeeded := int32(0)
			if job.Status.Failed != nil {
				failed = *job.Status.Failed
			}
			if job.Status.Succeeded != nil {
				succeeded = *job.Status.Succeeded
			}
			historyLines = append(historyLines, fmt.Sprintf("%s: %d failed, %d succeeded", job.Metadata.Name, failed, succeeded))
		}
	}

	content := strings.Join(historyLines, "\n")
	if err := o.writeFile(filepath.Join(o.outputDir, "16-job-history.txt"), content); err != nil {
		safeColorPrintf(red, "  ✗ Failed to collect job history\n\n")
	} else {
		safeColorPrintf(green, "  ✓ Saved to 16-job-history.txt\n\n")
	}
	return nil
}

func (o *pruningCronjobOptions) collectClusterVersion(yellow, green, red *color.Color) error {
	yellow.Println("Collecting: Cluster version information")
	if err := o.runOCCommand("get", []string{"clusterversion", "version", "-o", "yaml"}, filepath.Join(o.outputDir, "17-cluster-version.yaml")); err != nil {
		red.Printf("  ✗ Failed to collect cluster version\n\n")
	} else {
		green.Printf("  ✓ Saved to 17-cluster-version.yaml\n\n")
	}
	return nil
}

func (o *pruningCronjobOptions) generateSummary(clusterID string, green *color.Color) error {
	safeColorPrintln(green, "Generating summary report...")
	
	summaryFile := filepath.Join(o.outputDir, "00-SUMMARY.txt")
	summary := fmt.Sprintf(`PruningCronjobErrorSRE Diagnostic Collection Summary
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

	// Add jobs status
	jobsFile := filepath.Join(o.outputDir, "01-jobs.txt")
	if content, err := os.ReadFile(jobsFile); err == nil {
		summary += "\nJobs Status:\n"
		summary += string(content) + "\n"
	}

	// Add pods status
	podsFile := filepath.Join(o.outputDir, "02-pods.txt")
	if content, err := os.ReadFile(podsFile); err == nil {
		summary += "\nPods Status:\n"
		summary += string(content) + "\n"
	}

	// Add network type
	networkType, _ := o.runOCCommandWithOutput("get", []string{"Network.config.openshift.io", "cluster", "-o", "jsonpath={.spec.networkType}"})
	networkType = strings.TrimSpace(networkType)
	if networkType == "" {
		networkType = "Unable to determine"
	}
	summary += "\nNetwork Type:\n" + networkType + "\n\n"

	return o.writeFile(summaryFile, summary)
}

func (o *pruningCronjobOptions) createTarball() error {
	cmd := exec.Command("tar", "-czf", o.outputDir+".tar.gz", o.outputDir)
	return cmd.Run()
}

// extractAndReadDiagnostics extracts key diagnostic files and returns their content
func (o *pruningCronjobOptions) extractAndReadDiagnostics() (string, error) {
	var content strings.Builder

	// Priority files to read (in order)
	priorityFiles := []string{
		"00-SUMMARY.txt",
		"01-jobs.txt",
		"02-pods.txt",
		"14-seccomp-errors.txt",
		"16-job-history.txt",
		"05-events.txt",
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

	// Read pod logs and describe files (limit to first 5 failing pods to avoid token limits)
	podLogFiles, _ := filepath.Glob(filepath.Join(o.outputDir, "03-pod-logs-*.txt"))
	podDescribeFiles, _ := filepath.Glob(filepath.Join(o.outputDir, "04-pod-describe-*.txt"))

	// Sort and limit
	sort.Strings(podLogFiles)
	sort.Strings(podDescribeFiles)
	if len(podLogFiles) > 5 {
		podLogFiles = podLogFiles[:5]
	}
	if len(podDescribeFiles) > 5 {
		podDescribeFiles = podDescribeFiles[:5]
	}

	for _, filePath := range podLogFiles {
		if data, err := os.ReadFile(filePath); err == nil {
			content.WriteString(fmt.Sprintf("\n=== %s ===\n", filepath.Base(filePath)))
			// Limit log size to avoid token limits
			logContent := string(data)
			if len(logContent) > 10000 {
				logContent = logContent[:10000] + "\n... (truncated)"
			}
			content.WriteString(logContent)
			content.WriteString("\n")
		}
	}

	for _, filePath := range podDescribeFiles {
		if data, err := os.ReadFile(filePath); err == nil {
			content.WriteString(fmt.Sprintf("\n=== %s ===\n", filepath.Base(filePath)))
			content.Write(data)
			content.WriteString("\n")
		}
	}

	return content.String(), nil
}

// validateAPIKey performs basic validation on the API key format
func validateAPIKey(apiKey string) error {
	if apiKey == "" {
		return fmt.Errorf("API key is empty")
	}
	
	// Trim and check again
	trimmed := strings.TrimSpace(apiKey)
	if trimmed != apiKey {
		return fmt.Errorf("API key contains leading or trailing whitespace (length: %d -> %d after trim)", len(apiKey), len(trimmed))
	}
	
	// Basic length check - very short keys are likely invalid
	if len(apiKey) < 10 {
		return fmt.Errorf("API key appears too short (length: %d). Valid API keys are typically longer", len(apiKey))
	}
	
	// Don't reject based on format prefix - different providers use different formats
	// (e.g., OpenAI uses "sk-", some proxies use "sha256~", etc.)
	// Let the API call itself determine if the key is valid
	
	return nil
}

// analyzeWithLLM sends diagnostic content to LLM for analysis
func (o *pruningCronjobOptions) analyzeWithLLM(diagnosticContent string) (string, error) {
	systemPrompt := systemPromptTemplate
	// Fallback to default if embed failed (shouldn't happen, but be safe)
	if systemPrompt == "" {
		systemPrompt = `You are an expert OpenShift/Kubernetes Site Reliability Engineer (SRE) specializing in cluster maintenance and resource management. Your task is to analyze diagnostic information and assess the health of pruning cronjobs in the openshift-sre-pruning namespace on any OpenShift cluster.

Analyze the provided diagnostic data and provide:
1. Root cause analysis - What is likely causing any issues?
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
				"content": fmt.Sprintf("Please analyze the following pruning cronjob diagnostic information from an OpenShift cluster:\n\n%s", diagnosticContent),
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

// Helper functions for min/max
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
