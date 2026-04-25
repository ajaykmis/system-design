package main

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

// CommandStatus represents the execution status
type CommandStatus string

const (
	StatusPending   CommandStatus = "PENDING"
	StatusRunning   CommandStatus = "RUNNING"
	StatusCompleted CommandStatus = "COMPLETED"
	StatusFailed    CommandStatus = "FAILED"
	StatusCancelled CommandStatus = "CANCELLED"
)

// CommandResult holds the execution result
type CommandResult struct {
	CommandID   string
	Command     string
	Args        []string
	Status      CommandStatus
	Output      string
	ErrorOutput string
	ExitCode    int
	StartTime   time.Time
	EndTime     time.Time
	Duration    time.Duration
	Error       error
}

// CallbackFunc is the user-defined callback function
type CallbackFunc func(result *CommandResult)

// CommandHandle represents a submitted command
type CommandHandle struct {
	ID       string
	executor *CommandExecutor
	cancel   context.CancelFunc
	callback CallbackFunc
}

// GetStatus returns current command status
func (ch *CommandHandle) GetStatus() CommandStatus {
	ch.executor.mu.RLock()
	defer ch.executor.mu.RUnlock()

	if result, exists := ch.executor.commands[ch.ID]; exists {
		return result.Status
	}
	return StatusFailed
}

// GetResult returns the command result (blocking until complete)
func (ch *CommandHandle) GetResult() *CommandResult {
	return ch.WaitForCompletion()
}

// GetResultNonBlocking returns result if available, nil otherwise
func (ch *CommandHandle) GetResultNonBlocking() *CommandResult {
	ch.executor.mu.RLock()
	defer ch.executor.mu.RUnlock()

	if result, exists := ch.executor.commands[ch.ID]; exists {
		if result.Status == StatusCompleted || result.Status == StatusFailed || result.Status == StatusCancelled {
			return result
		}
	}
	return nil
}

// WaitForCompletion blocks until command completes
func (ch *CommandHandle) WaitForCompletion() *CommandResult {
	for {
		if result := ch.GetResultNonBlocking(); result != nil {
			return result
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Cancel attempts to cancel the running command
func (ch *CommandHandle) Cancel() bool {
	ch.cancel()

	// Update status
	ch.executor.mu.Lock()
	defer ch.executor.mu.Unlock()

	if result, exists := ch.executor.commands[ch.ID]; exists {
		if result.Status == StatusPending || result.Status == StatusRunning {
			result.Status = StatusCancelled
			result.EndTime = time.Now()
			result.Duration = result.EndTime.Sub(result.StartTime)
			return true
		}
	}
	return false
}

// CommandExecutor manages command execution
type CommandExecutor struct {
	commands  map[string]*CommandResult
	mu        sync.RWMutex
	idCounter int64
	idMutex   sync.Mutex
}

// NewCommandExecutor creates a new command executor
func NewCommandExecutor() *CommandExecutor {
	return &CommandExecutor{
		commands: make(map[string]*CommandResult),
	}
}

// generateID creates a unique command ID
func (ce *CommandExecutor) generateID() string {
	ce.idMutex.Lock()
	defer ce.idMutex.Unlock()
	ce.idCounter++
	return fmt.Sprintf("cmd_%d_%d", time.Now().Unix(), ce.idCounter)
}

// Submit submits a command for execution
func (ce *CommandExecutor) Submit(command string, args []string, callback CallbackFunc) *CommandHandle {
	commandID := ce.generateID()

	// Create command result
	result := &CommandResult{
		CommandID: commandID,
		Command:   command,
		Args:      args,
		Status:    StatusPending,
		StartTime: time.Now(),
	}

	// Store in map
	ce.mu.Lock()
	ce.commands[commandID] = result
	ce.mu.Unlock()

	// Create context for cancellation
	ctx, cancel := context.WithCancel(context.Background())

	// Create handle
	handle := &CommandHandle{
		ID:       commandID,
		executor: ce,
		cancel:   cancel,
		callback: callback,
	}

	// Start execution in goroutine
	go ce.executeCommand(ctx, result, callback)

	return handle
}

// executeCommand runs the actual command
func (ce *CommandExecutor) executeCommand(ctx context.Context, result *CommandResult, callback CallbackFunc) {
	// Update status to running
	ce.updateStatus(result.CommandID, StatusRunning)

	// Create command with context for cancellation
	cmd := exec.CommandContext(ctx, result.Command, result.Args...)

	// Capture output
	output, err := cmd.CombinedOutput()

	// Update result
	ce.mu.Lock()
	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)
	result.Output = string(output)

	if ctx.Err() == context.Canceled {
		result.Status = StatusCancelled
	} else if err != nil {
		result.Status = StatusFailed
		result.Error = err
		if exitError, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitError.ExitCode()
		}
	} else {
		result.Status = StatusCompleted
		result.ExitCode = 0
	}
	ce.mu.Unlock()

	// Execute callback if provided
	if callback != nil {
		go callback(result)
	}
}

// updateStatus updates command status thread-safely
func (ce *CommandExecutor) updateStatus(commandID string, status CommandStatus) {
	ce.mu.Lock()
	defer ce.mu.Unlock()

	if result, exists := ce.commands[commandID]; exists {
		result.Status = status
	}
}

// GetAllCommands returns all command results
func (ce *CommandExecutor) GetAllCommands() map[string]*CommandResult {
	ce.mu.RLock()
	defer ce.mu.RUnlock()

	// Create a copy to avoid external modifications
	copy := make(map[string]*CommandResult)
	for k, v := range ce.commands {
		copy[k] = v
	}
	return copy
}

// CleanupCompleted removes completed commands from memory
func (ce *CommandExecutor) CleanupCompleted() int {
	ce.mu.Lock()
	defer ce.mu.Unlock()

	cleaned := 0
	for id, result := range ce.commands {
		if result.Status == StatusCompleted || result.Status == StatusFailed || result.Status == StatusCancelled {
			delete(ce.commands, id)
			cleaned++
		}
	}
	return cleaned
}

// Example usage and demonstrations
func main() {
	executor := NewCommandExecutor()

	fmt.Println("=== Command Execution System Demo ===")

	// Example 1: Simple command with callback
	fmt.Println("1. Simple command with callback:")
	handle1 := executor.Submit("echo", []string{"Hello, World!"}, func(result *CommandResult) {
		fmt.Printf("   Callback: Command %s completed with status: %s\n", result.CommandID, result.Status)
		fmt.Printf("   Output: %s", result.Output)
	})

	// Wait for completion
	result1 := handle1.WaitForCompletion()
	fmt.Printf("   Final result: %s (took %v)\n\n", result1.Status, result1.Duration)

	// Example 2: Long-running command with status checking
	fmt.Println("2. Long-running command with status monitoring:")
	handle2 := executor.Submit("sleep", []string{"3"}, func(result *CommandResult) {
		fmt.Printf("   Sleep command finished with status: %s\n", result.Status)
	})

	// Monitor status
	for i := 0; i < 5; i++ {
		status := handle2.GetStatus()
		fmt.Printf("   Status check %d: %s\n", i+1, status)
		if status == StatusCompleted || status == StatusFailed {
			break
		}
		time.Sleep(1 * time.Second)
	}

	// Example 3: Command that fails
	fmt.Println("\n3. Command that fails:")
	handle3 := executor.Submit("invalidcommand", []string{}, func(result *CommandResult) {
		fmt.Printf("   Failed command callback: %s, Error: %v\n", result.Status, result.Error)
	})

	result3 := handle3.WaitForCompletion()
	fmt.Printf("   Failed command result: %s\n\n", result3.Status)

	// Example 4: Command cancellation
	fmt.Println("4. Command cancellation:")
	handle4 := executor.Submit("sleep", []string{"10"}, func(result *CommandResult) {
		fmt.Printf("   Cancelled command callback: %s\n", result.Status)
	})

	time.Sleep(500 * time.Millisecond)
	fmt.Printf("   Cancelling command...\n")
	cancelled := handle4.Cancel()
	fmt.Printf("   Cancellation successful: %t\n", cancelled)

	result4 := handle4.WaitForCompletion()
	fmt.Printf("   Final status: %s\n\n", result4.Status)

	// Example 5: Multiple concurrent commands
	fmt.Println("5. Multiple concurrent commands:")
	var handles []*CommandHandle

	commands := [][]string{
		{"echo", "command 0"},
		{"ls", "-lrt"},
		{"sleep", "2"},
		{"echo", "command 3"},
		{"sleep", "1"},
		{"echo", "command 5"},
	}

	for _, cmd := range commands {
		handle := executor.Submit(cmd[0], cmd[1:], func(result *CommandResult) {
			fmt.Printf("   Concurrent callback: %s completed\n", result.CommandID)
		})
		handles = append(handles, handle)
	}

	// Wait for all to complete
	for _, handle := range handles {
		handle.WaitForCompletion()
	}

	// Show all commands
	fmt.Println("\n6. All executed commands:")
	allCommands := executor.GetAllCommands()
	for id, result := range allCommands {
		fmt.Printf("   %s: %s %v -> %s (took %v)\n",
			id, result.Command, result.Args, result.Status, result.Duration)
	}

	// Cleanup
	cleaned := executor.CleanupCompleted()
	fmt.Printf("\nCleaned up %d completed commands\n", cleaned)
}
