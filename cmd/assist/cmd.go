package assist

import (
	"fmt"

	"github.com/spf13/cobra"
)

// NewCmdAssist implements the assist command
func NewCmdAssist() *cobra.Command {
	assistCmd := &cobra.Command{
		Use:   "assist",
		Short: "Assist commands for troubleshooting alerts and issues",
		Long: `Assist commands provide automated diagnostic collection and troubleshooting assistance for various alerts and operational issues.

These commands gather comprehensive diagnostic information to help SRE teams quickly
identify and resolve problems. Each assist command is designed for a specific alert
or issue type and collects all relevant information based on standard operating procedures.`,
		Args:              cobra.NoArgs,
		DisableAutoGenTag: true,
		Run:               help,
	}

	assistCmd.AddCommand(NewCmdPruningCronjobErrorSRE())
	assistCmd.AddCommand(NewCmdClusterMonitoringErrorBudgetBurnSRE())
	assistCmd.AddCommand(NewCmdDynatraceMonitoringStackDownSRE())
	assistCmd.AddCommand(NewCmdClusterProvisioningFailure())

	return assistCmd
}

func help(cmd *cobra.Command, _ []string) {
	err := cmd.Help()
	if err != nil {
		fmt.Println("error in assist command: ", err.Error())
		return
	}
}
