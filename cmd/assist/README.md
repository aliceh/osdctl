# osdctl assist - AI-Powered Diagnostic Collection & Analysis

## Overview

The `osdctl assist` commands provide automated diagnostic collection and AI-powered analysis for OpenShift SRE alerts and cluster health investigations. These commands streamline incident response by:

- **Automating diagnostic collection** - Gathering all relevant data for specific alerts or cluster issues
- **AI-powered analysis** - Using LLM to identify root causes, key findings, and recommended actions
- **Interactive investigation** - Allowing SREs to ask follow-up questions based on diagnostic context
- **Offline analysis** - Supporting analysis of previously collected diagnostic bundles

## Architecture

### Command Structure

```
osdctl assist [command] [flags]
```

Commands are organized by category:

- **Alert-specific commands**: Map to specific SRE alerts (e.g., `pruning-cronjob-error-sre`)
- **Health check commands**: Target cluster components (e.g., `etcd`, `control-plane`)
- **Installation commands**: For provisioning and installation failures (e.g., `cluster-provisioning-failure`)

### Core Components

```
cmd/assist/
├── README.md                           # This file
├── cmd.go                              # Root command definition
├── <command-name>.go                   # Individual command implementations
├── <command-name>_test.go              # Unit tests for commands
└── prompts/
    └── <command-name>_analysis.md      # LLM analysis prompts
```

### Common Patterns

All assist commands follow a consistent pattern:

1. **Command Definition** - Cobra command with standardized flags
2. **Options Struct** - Configuration and state for the command
3. **Complete Phase** - Validate inputs, resolve configuration
4. **Run Phase** - Execute diagnostic collection and analysis
5. **Collection Functions** - Gather specific diagnostic data
6. **Summary Generation** - Create human-readable overview
7. **LLM Analysis** - AI-powered root cause analysis

## Implementation Guide

### Creating a New Assist Command

#### 1. Create Command File

Create `<alert-name>.go` with the following structure:

```go
package assist

import (
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "time"
    
    "github.com/fatih/color"
    "github.com/spf13/cobra"
    cmdutil "k8s.io/kubectl/pkg/cmd/util"
)

//go:embed prompts/<alert-name>_analysis.md
var <alertName>SystemPromptTemplate string

type <alertName>Options struct {
    outputDir      string
    existingDir    string
    skipCollection bool
    // LLM options
    enableLLMAnalysis bool
    llmAPIKey         string
    llmBaseURL        string
    llmModel          string
    // For testing
    commandExecutor func(name string, args []string, outputFile string) error
    commandRunner   func(name string, args []string) (string, error)
}

func NewCmd<AlertName>() *cobra.Command {
    ops := new<AlertName>Options()
    cmd := &cobra.Command{
        Use:   "<alert-name>",
        Short: "Collect diagnostic information for <AlertName> alert",
        Long:  `Detailed description...`,
        Example: `Examples...`,
        Args:              cobra.NoArgs,
        DisableAutoGenTag: true,
        Run: func(cmd *cobra.Command, args []string) {
            cmdutil.CheckErr(ops.complete(cmd, args))
            cmdutil.CheckErr(ops.run())
        },
    }
    
    // Standard flags
    cmd.Flags().StringVar(&ops.outputDir, "output-dir", "", "Output directory")
    cmd.Flags().StringVar(&ops.existingDir, "analyze-existing", "", "Analyze existing directory")
    cmd.Flags().BoolVar(&ops.enableLLMAnalysis, "analyze", false, "Enable LLM analysis")
    cmd.Flags().StringVar(&ops.llmAPIKey, "llm-api-key", "", "LLM API key")
    cmd.Flags().StringVar(&ops.llmBaseURL, "llm-base-url", "", "LLM API base URL")
    cmd.Flags().StringVar(&ops.llmModel, "llm-model", "gpt-4o-mini", "LLM model")
    
    return cmd
}
```

#### 2. Implement Core Methods

**Complete Method** - Validate and configure:

```go
func (o *<alertName>Options) complete(cmd *cobra.Command, _ []string) error {
    // Handle existing directory analysis
    if o.existingDir != "" {
        // Validate directory exists
        if info, err := os.Stat(o.existingDir); err != nil {
            return fmt.Errorf("directory does not exist: %w", err)
        } else if !info.IsDir() {
            return fmt.Errorf("not a directory: %s", o.existingDir)
        }
        o.outputDir = o.existingDir
        o.skipCollection = true
        o.enableLLMAnalysis = true
    } else if o.outputDir == "" {
        // Generate timestamped directory
        timestamp := time.Now().Format("20060102-150405")
        o.outputDir = fmt.Sprintf("<alert-name>-diagnostics-%s", timestamp)
    }
    
    // Configure LLM settings (see loadConfigValue helper)
    if o.enableLLMAnalysis {
        // Load from config file, env vars, or flags
        // Validate API key
    }
    
    return nil
}
```

**Run Method** - Main execution flow:

```go
func (o *<alertName>Options) run() error {
    green := color.New(color.FgGreen)
    yellow := color.New(color.FgYellow)
    red := color.New(color.FgRed)
    
    if o.skipCollection {
        // Analyze existing directory
        green.Println("Analyzing existing diagnostic artifacts...")
    } else {
        // Collect diagnostics
        green.Println("Collecting diagnostic information...")
        
        // Create output directory
        if err := os.MkdirAll(o.outputDir, 0755); err != nil {
            return err
        }
        
        // Check prerequisites (oc, cluster access, etc.)
        
        // Collect all diagnostic information
        if err := o.collectComponentA(yellow, green, red); err != nil {
            return err
        }
        if err := o.collectComponentB(yellow, green, red); err != nil {
            return err
        }
        
        // Generate summary
        if err := o.generateSummary(green); err != nil {
            return err
        }
        
        // Create tarball
        if err := o.createTarball(); err != nil {
            // Non-fatal
        }
    }
    
    // LLM Analysis (if enabled)
    if o.enableLLMAnalysis {
        // Extract diagnostics, analyze with LLM, interactive Q&A
    }
    
    // Print next steps
    return nil
}
```

#### 3. Implement Collection Functions

Each collection function should:
- Use color output for status updates
- Handle errors gracefully (continue on non-critical failures)
- Save output to numbered files (e.g., `01-component.txt`)
- Include both text and YAML/JSON formats where useful

```go
func (o *<alertName>Options) collectComponentA(yellow, green, red *color.Color) error {
    safeColorPrintln(yellow, "Collecting: Component A status")
    
    outputFile := filepath.Join(o.outputDir, "01-component-a.txt")
    if err := o.runOCCommand("get", []string{"resource", "-o", "wide"}, outputFile); err != nil {
        safeColorPrintf(red, "  ✗ Failed to collect Component A\n\n")
        return err // or continue for non-critical
    }
    
    safeColorPrintf(green, "  ✓ Saved to 01-component-a.txt\n\n")
    return nil
}
```

#### 4. Create LLM Analysis Prompt

Create `prompts/<alert-name>_analysis.md`:

```markdown
# <Alert Name> Analysis System Prompt

You are an expert OpenShift/Kubernetes SRE specializing in [specific area].

## Your Expertise
- [Domain knowledge areas]
- [Common failure patterns]
- [Troubleshooting techniques]

## Analysis Framework
When analyzing diagnostics:
1. Assess overall system state
2. Identify failing components
3. Trace dependencies and cascading failures
4. Look for common patterns

## Output Format
Provide analysis in this structure:

### 🔍 **Summary**
Brief overview of the issue

### 🎯 **Root Cause**
Clear explanation with evidence

### 📊 **Key Findings**
- Finding 1
- Finding 2

### ⚠️ **Severity Assessment**
Critical / High / Medium / Low

### 🔧 **Recommended Actions**
1. Step 1
2. Step 2

### 📚 **Additional Context**
Related information, caveats, links
```

#### 5. Add Tests

Create `<alert-name>_test.go`:

```go
func TestNewCmd<AlertName>(t *testing.T) {
    cmd := NewCmd<AlertName>()
    assert.NotNil(t, cmd)
    assert.Equal(t, "<alert-name>", cmd.Use)
}

func Test<AlertName>Options_complete(t *testing.T) {
    // Test configuration validation
}

func Test<AlertName>Options_collectComponentA(t *testing.T) {
    tmpDir := t.TempDir()
    ops := new<AlertName>Options()
    ops.outputDir = tmpDir
    
    ops.commandExecutor = func(name string, args []string, outputFile string) error {
        return os.WriteFile(outputFile, []byte("test output"), 0644)
    }
    
    err := ops.collectComponentA(nil, nil, nil)
    assert.NoError(t, err)
    
    // Verify file was created
    _, err = os.Stat(filepath.Join(tmpDir, "01-component-a.txt"))
    assert.NoError(t, err)
}
```

#### 6. Register Command

Add to `cmd.go`:

```go
assistCmd.AddCommand(NewCmd<AlertName>())
```

## Standard Flags

All assist commands should support these flags:

| Flag | Type | Description | Default |
|------|------|-------------|---------|
| `--output-dir` | string | Output directory for diagnostic files | `<command>-diagnostics-TIMESTAMP` |
| `--analyze-existing` | string | Path to existing diagnostic directory to analyze | - |
| `--analyze` | bool | Enable LLM analysis of collected diagnostics | `false` |
| `--llm-api-key` | string | LLM API key (or use config/env) | - |
| `--llm-base-url` | string | LLM API endpoint | `https://api.openai.com/v1` |
| `--llm-model` | string | Model name | `gpt-4o-mini` |

## Configuration

Commands respect this priority order:
1. Command-line flags
2. `~/.config/osdctl` configuration file
3. Environment variables
4. Defaults

### Configuration File Format

`~/.config/osdctl`:
```yaml
OPENAI_API_KEY: "sk-..."
OPENAI_BASE_URL: "https://api.openai.com/v1"
AI_MODEL_NAME: "gpt-4o-mini"
```

### Environment Variables

- `OPENAI_API_KEY` / `LLM_API_KEY` - API key
- `OPENAI_BASE_URL` / `LLM_BASE_URL` - API endpoint
- `AI_MODEL_NAME` / `LLM_MODEL` - Model name

## File Naming Conventions

Diagnostic files should use numbered prefixes for consistent ordering:

```
00-SUMMARY.txt                    # Overview and key information
01-<primary-resource>.txt         # Main resource being investigated
01-<primary-resource>.yaml        # Detailed YAML output
02-<secondary-resource>.txt       # Related resources
03-<component>-describe-*.txt     # Describe output for failing components
...
10-events-*.txt                   # Events from relevant namespaces
11-logs-*.txt                     # Logs from relevant components
18-llm-analysis.txt               # AI analysis output (if enabled)
```

## Helper Functions

### Safe Color Printing

Use these helpers to ensure color output works in tests:

```go
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
```

### Configuration Loading

```go
func loadConfigValue(configKey, envVarName, defaultValue string) string {
    // Try config file first
    homeDir, err := os.UserHomeDir()
    if err == nil {
        configPath := filepath.Join(homeDir, ".config", "osdctl")
        if _, err := os.Stat(configPath); err == nil {
            v := viper.New()
            v.SetConfigFile(configPath)
            v.SetConfigType("yaml")
            if err := v.ReadInConfig(); err == nil {
                if value := v.GetString(configKey); value != "" {
                    return strings.TrimSpace(strings.Trim(value, "\"'"))
                }
            }
        }
    }
    
    // Fall back to environment
    if envVarName != "" {
        if envValue := os.Getenv(envVarName); envValue != "" {
            return strings.TrimSpace(envValue)
        }
    }
    
    return defaultValue
}
```

## LLM Integration

### Analysis Flow

1. **Extract Diagnostics** - Read key diagnostic files
2. **Build Context** - Format for LLM input with size limits
3. **API Call** - Send to LLM with system prompt and diagnostics
4. **Parse Response** - Extract analysis and recommendations
5. **Interactive Q&A** - Allow follow-up questions with context

### Token Management

- Limit individual file sizes (10-20KB)
- Truncate logs intelligently (beginning + end)
- Prioritize critical files over verbose logs
- Include file names in context for reference

### Implementation Pattern

```go
func (o *<alertName>Options) analyzeWithLLM(diagnosticContent string) (string, []Message, error) {
    systemPrompt := <alertName>SystemPromptTemplate
    
    conversationHistory := []Message{
        {Role: "system", Content: systemPrompt},
        {Role: "user", Content: fmt.Sprintf("Analyze: %s", diagnosticContent)},
    }
    
    requestBody := map[string]interface{}{
        "model":       o.llmModel,
        "messages":    conversationHistory,
        "temperature": 0.3,
        "max_tokens":  4000,
    }
    
    // Make API call
    // Parse response
    // Return analysis and updated conversation history
}
```

## Testing Guidelines

### Unit Tests

- Test command creation and flag parsing
- Test option validation in `complete()`
- Mock `oc` command execution for collection functions
- Verify file creation and content
- Test error handling

### Test Fixtures

Create test data for:
- Sample `oc` command outputs
- Diagnostic file collections
- LLM API responses (mock)

### Mock Executors

Inject mock executors for testing:

```go
ops.commandExecutor = func(name string, args []string, outputFile string) error {
    return os.WriteFile(outputFile, []byte("mock output"), 0644)
}

ops.commandRunner = func(name string, args []string) (string, error) {
    return "mock output", nil
}
```

## Special Cases

### Cluster Not Accessible

For installation failures or provisioning issues, the cluster may not be accessible. Handle this by:

1. Using OCM API instead of `oc` commands
2. Collecting install logs via `ocm get /api/clusters_mgmt/v1/clusters/<ID>/resources`
3. Providing clear instructions for manual log collection
4. Creating helper notes for SREs

Example: `cluster-provisioning-failure` command

### Multiple Namespaces

When collecting from multiple namespaces:
- Use consistent file naming with namespace suffix
- Collect in order of priority
- Handle namespace-not-found errors gracefully

### Large Output

For commands that produce large output:
- Use `--tail` flags to limit log size
- Truncate in summary generation
- Store full output in separate files

## Existing Commands

### Alert-Specific Commands

| Command | Alert | Description |
|---------|-------|-------------|
| `pruning-cronjob-error-sre` | PruningCronjobErrorSRE | Diagnoses pruning cronjob failures in openshift-sre-pruning |
| `cluster-monitoring-error-budget-burn-sre` | ClusterMonitoringErrorBudgetBurnSRE | Analyzes monitoring error budget burn issues |
| `dynatrace-monitoring-stack-down-sre` | DynatraceMonitoringStackDownSRE | Troubleshoots Dynatrace monitoring stack issues |
| `cluster-provisioning-failure` | ClusterProvisioningFailure | Collects install logs for failed cluster provisioning |

## Usage Examples

### Basic Collection

```bash
# Collect diagnostics for an alert
osdctl assist pruning-cronjob-error-sre

# Output:
# Collecting diagnostic information...
# Output directory: pruning-cronjob-diagnostics-20240118-143022
# ✓ Saved to 01-jobs.txt
# ✓ Saved to 02-pods.txt
# ...
```

### With AI Analysis

```bash
# Collect and analyze
osdctl assist pruning-cronjob-error-sre --analyze

# Output includes:
# === LLM Analysis Results ===
# [AI-generated root cause analysis]
# [Follow-up Q&A session]
```

### Analyze Existing Diagnostics

```bash
# Analyze previously collected diagnostics
osdctl assist pruning-cronjob-error-sre --analyze-existing ./diagnostics-dir

# Skips collection, goes straight to AI analysis
```

### Custom Configuration

```bash
# Use custom LLM configuration
osdctl assist pruning-cronjob-error-sre \
  --analyze \
  --llm-model gpt-4 \
  --llm-base-url https://custom-llm-endpoint.com/v1
```

## Development Workflow

### Adding a New Command

1. Identify the alert/issue to address
2. Review SOP documentation for troubleshooting steps
3. Create command file with collection functions
4. Write LLM analysis prompt
5. Add unit tests
6. Register command in `cmd.go`
7. Test manually with real cluster (if accessible)
8. Document in this README

### Code Review Checklist

- [ ] Follows standard command structure
- [ ] Includes all standard flags
- [ ] Has comprehensive tests (>80% coverage)
- [ ] Includes LLM analysis prompt
- [ ] Handles errors gracefully
- [ ] Uses consistent file naming
- [ ] Updates this README
- [ ] Links to relevant SOP documentation

## Architecture Decisions

### Why Cobra?

Cobra provides excellent command-line structure with:
- Automatic help generation
- Flag parsing and validation
- Subcommand organization
- Wide adoption in Go ecosystem

### Why Embedded Prompts?

Embedding LLM prompts in the binary:
- Ensures prompts are versioned with code
- Simplifies deployment (single binary)
- Allows for prompt iteration with code changes
- Makes prompts reviewable in PRs

### Why Timestamped Directories?

Timestamped output directories:
- Prevent overwriting previous diagnostics
- Allow multiple collections for comparison
- Make organization intuitive
- Support historical analysis

### Why Not Stream LLM Responses?

We use complete responses rather than streaming:
- Simpler error handling
- Easier to save complete analysis
- Better for interactive Q&A context
- Most responses are fast enough (<30s)

## Future Enhancements

### Short Term

- [ ] Parallel diagnostic collection for performance
- [ ] Progress indicators for long-running collections
- [ ] Structured output format (JSON) for programmatic use
- [ ] Diagnostic collection timeout handling

### Medium Term

- [ ] Plugin system for custom commands
- [ ] Diagnostic result caching to reduce cluster load
- [ ] Multi-cluster collection support
- [ ] Enhanced error messages with troubleshooting hints

### Long Term

- [ ] Web UI for diagnostic visualization
- [ ] Automated remediation suggestions
- [ ] Integration with ticketing systems
- [ ] Telemetry and usage analytics
- [ ] Local LLM support for sensitive environments

## Troubleshooting

### LLM Analysis Fails

**Problem**: API key invalid or network issues

**Solutions**:
1. Verify API key: `echo $OPENAI_API_KEY`
2. Check connectivity: `curl https://api.openai.com/v1/models -H "Authorization: Bearer $KEY"`
3. Review base URL configuration
4. Check for proxy requirements

### Collection Fails

**Problem**: Not logged into cluster

**Solutions**:
1. Login: `ocm backplane login <cluster-id>`
2. Verify: `oc whoami`
3. For provisioning failures, use `--cluster` flag instead

### Files Not Generated

**Problem**: Permission issues or disk space

**Solutions**:
1. Check write permissions on output directory
2. Verify disk space: `df -h`
3. Try custom output dir: `--output-dir /tmp/diagnostics`

## Contributing

### Getting Started

1. Clone repository and create feature branch
2. Review existing commands for patterns
3. Implement your command following this guide
4. Add comprehensive tests
5. Update this README with your command
6. Submit pull request

### Code Standards

- Follow Go best practices and conventions
- Use meaningful variable and function names
- Add comments for complex logic
- Keep functions focused and testable
- Handle errors explicitly

### Testing Requirements

- Unit tests for all collection functions
- Integration tests for complete workflows
- Mock external dependencies (`oc`, LLM APIs)
- Achieve >80% code coverage
- Test error conditions and edge cases

## References

- **SOP Documentation**: `~/ops-sop/v4/alerts/`
- **JIRA Epic**: [SREP-2909](https://issues.redhat.com/browse/SREP-2909)
- **OpenShift CLI**: https://docs.openshift.com/container-platform/latest/cli_reference/openshift_cli/getting-started-cli.html
- **Cobra**: https://github.com/spf13/cobra

## License

This project follows the osdctl project license.

---

**Last Updated**: December 2024

**Maintainers**: SRE Team

**Questions?** Contact the SRE team or open an issue.

