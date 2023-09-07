package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"gopkg.in/yaml.v2"
)

type Task struct {
	Name     string   `yaml:"name"`
	Cmds     []string `yaml:"cmds"`
	Silent   bool     `yaml:"silent"`
	Parallel bool     `yaml:"parallel"` // Add the parallel field for each task
	Required []string `yaml:"required"` 
}

type Config struct {
	Vars  map[string]string `yaml:"vars"`
	Usage string            `yaml:"usage"` // Add the usage field
	Tasks []Task            `yaml:"modules"`
}

var moduleSyncChan = make(chan struct{}, 1)

func main() {
	var (
		taskFile  string
		variables map[string]string
		quietMode bool // Flag to indicate quiet mode
	)

	flag.StringVar(&taskFile, "w", "", "Path to the workflow YAML file")
	flag.BoolVar(&quietMode, "q", false, "Suppress banner")
	flag.Parse()
	log.SetFlags(0)

	// Color formatting functions
	cyan := color.New(color.FgCyan).SprintFunc()
	yellow := color.New(color.FgYellow).SprintFunc()
	red := color.New(color.FgRed).SprintFunc()
	green := color.New(color.FgGreen).SprintFunc()
	white := color.New(color.FgWhite).SprintFunc()
	magenta := color.New(color.FgMagenta).SprintFunc()

	// Print banner
	if !quietMode {
		// Print banner only if quiet mode is not enabled
		fmt.Fprintf(os.Stderr,"\n%s\n\n", white(`
	                         __         
	   _____________  ______/ /__  _____
	  / ___/ __  / / / / __  / _ \/ ___/
	 / /  / /_/ / /_/ / /_/ /  __/ /    
	/_/   \____/\___ /\____/\___/_/     
	           /____/                   

	           		- v0.0.4 ⚡

`))

	}

	var defaultVars map[string]string
	yamlFileContent, err := ioutil.ReadFile(taskFile)
	if err == nil {
		var config Config
		err = yaml.Unmarshal(yamlFileContent, &config)
		if err == nil {
			defaultVars = config.Vars
		}
	}

	variables = parseArgs(defaultVars)

	if taskFile == "" {
		fmt.Fprintln(os.Stderr,"Usage: rayder -w workflow.yaml [variable assignments e.g. DOMAIN=example.host]")
		return
	}

	taskFileContent, err := ioutil.ReadFile(taskFile)
	if err != nil {
		log.Fatalf("Error reading workflow file: %v", err)
	}

	var config Config
	err = yaml.Unmarshal(taskFileContent, &config)
	if err != nil {
		log.Fatalf("Error unmarshaling YAML: %v", err)
	}

	runAllTasks(config, variables, cyan, magenta, white, yellow, red, green)
}

func parseArgs(defaultVars map[string]string) map[string]string {
	variables := make(map[string]string)
	usageRequested := false

	for _, arg := range flag.Args() {
		if arg == "usage" || arg == "USAGE" {
			usageRequested = true
			break
		}

		parts := strings.SplitN(arg, "=", 2)
		if len(parts) == 2 {
			if variables == nil {
				variables = make(map[string]string)
			}
			variables[parts[0]] = parts[1]
		}
	}

	// Check if "usage" was requested
	if usageRequested {
		fmt.Fprintln(os.Stderr,"Usage:")
		fmt.Fprintln(os.Stderr,defaultVars["USAGE"])

		fmt.Fprintln(os.Stderr,"\nVariables from YAML:")
		for key, value := range defaultVars {
			if key != "USAGE" {
				fmt.Fprintf(os.Stderr,"%s: %s\n", key, value)
			}
		}

		os.Exit(0)
	}

	// Apply default values if not provided by the user
	for key, defaultValue := range defaultVars {
		if _, exists := variables[key]; !exists {
			variables[key] = defaultValue
		}
	}

	return variables
}

func runAllTasks(config Config, variables map[string]string, cyan, magenta, white, yellow, red, green func(a ...interface{}) string) {
    var wg sync.WaitGroup
    var errorOccurred bool
    var errorMutex sync.Mutex

    // Create a map to track task completion
    taskCompleted := make(map[string]bool)

    for _, task := range config.Tasks {
        if len(task.Required) > 0 {
            // Check if all required tasks are completed before running this task
            allRequiredCompleted := true
            for _, req := range task.Required {
                if !taskCompleted[req] {
                    allRequiredCompleted = false
                    break
                }
            }

            if !allRequiredCompleted {
                // Skip the task if required tasks are not completed
                fmt.Fprintf(os.Stderr, "[%s] [%s] Skipping Module '%s' because required tasks are incomplete\n", yellow(currentTime()), red("INFO"), cyan(task.Name))
                continue
            }
        }

        if task.Parallel {
            // Use the moduleSyncChan to synchronize parallel executions
            moduleSyncChan <- struct{}{}
            wg.Add(1)
            go func(name string, cmds []string, silent bool, vars map[string]string) {
                defer func() {
                    <-moduleSyncChan
                    wg.Done()
                }()
                err := runTask(name, cmds, silent, vars, cyan, magenta, white, yellow, red, green)
                if err != nil {
                    errorMutex.Lock()
                    errorOccurred = true
                    fmt.Fprintf(os.Stderr, "[%s] [%s] Module '%s' %s ❌\n", yellow(currentTime()), red("INFO"), cyan(name), red("errored"))
                    errorMutex.Unlock()
                }
                // Signal the completion of this task
                taskCompleted[name] = true
            }(task.Name, task.Cmds, task.Silent, variables)
        } else {
            err := runTask(task.Name, task.Cmds, task.Silent, variables, cyan, magenta, white, yellow, red, green)
            if err != nil {
                errorOccurred = true
                fmt.Fprintf(os.Stderr, "[%s] [%s] Module '%s' %s ❌\n", yellow(currentTime()), red("INFO"), cyan(task.Name), red("errored"))
            }
            // Signal the completion of this task
            taskCompleted[task.Name] = true
        }
    }

    wg.Wait() // Wait for all parallel tasks to finish

    if errorOccurred {
        fmt.Fprintf(os.Stderr, "[%s] [%s] Errors occurred during execution. Exiting program ❌\n", yellow(currentTime()), red("INFO"))
        os.Exit(1) // Exit with error code 1
    }

    fmt.Fprintf(os.Stderr, "[%s] [%s] All modules completed successfully ✅\n", yellow(currentTime()), yellow("INFO"))
}


func runTask(taskName string, cmds []string, silent bool, vars map[string]string, cyan, magenta, white, yellow, red, green func(a ...interface{}) string) error {
	currentTime()
	fmt.Fprintf(os.Stderr,"[%s] [%s] Module '%s' %s ⚡\n", yellow(currentTime()), yellow("INFO"), cyan(taskName), yellow("running"))

	var hasError bool
	for _, cmd := range cmds {
		err := executeCommand(cmd, silent, vars)
		if err != nil {
			hasError = true
			break
		}
	}

	if hasError {
		return fmt.Errorf("Module '%s' %s ❌", taskName, red("errored"))
	}

	fmt.Fprintf(os.Stderr,"[%s] [%s] Module '%s' %s ✅\n", yellow(currentTime()), yellow("INFO"), cyan(taskName), green("completed"))
	return nil
}

func executeCommand(cmdStr string, silent bool, vars map[string]string) error {
	cmdStr = replacePlaceholders(cmdStr, vars)
	execCmd := exec.Command("sh", "-c", cmdStr)

	if silent {
		execCmd.Stdout = nil
		execCmd.Stderr = nil
	} else {
		execCmd.Stdout = os.Stdout
		execCmd.Stderr = os.Stderr
	}

	err := execCmd.Run()
	if err != nil {
		return fmt.Errorf("command execution failed: %w", err)
	}
	return nil
}

func replacePlaceholders(input string, vars map[string]string) string {
	for key, value := range vars {
		placeholder := fmt.Sprintf("{{%s}}", key)
		input = strings.ReplaceAll(input, placeholder, value)
	}
	return input
}

func currentTime() string {
	return time.Now().Format("2006-01-02 15:04:05")
}
