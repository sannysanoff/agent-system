package prompts

import (
	"fmt"
	"os"
	"strings"
)

var verboseLogger *VerboseLogger

func init() {
	verboseLogger = &VerboseLogger{
		enabled: isVerbose(),
	}
}

type VerboseLogger struct {
	enabled bool
}

func isVerbose() bool {
	verbose := os.Getenv("VERBOSE")
	return strings.ToLower(verbose) == "1" || strings.ToLower(verbose) == "true"
}

func IsVerbose() bool {
	return verboseLogger.enabled
}

func (v *VerboseLogger) Logf(format string, args ...interface{}) {
	if v.enabled {
		fmt.Printf("[VERBOSE] "+format+"\n", args...)
	}
}

func VerboseLog(format string, args ...interface{}) {
	verboseLogger.Logf(format, args...)
}
