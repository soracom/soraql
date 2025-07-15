package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/c-bata/go-prompt"
)

type Config struct {
	Email        string            `json:"email"`
	Password     string            `json:"password"`
	AuthKeyId    string            `json:"authKeyId"`
	AuthKey      string            `json:"authKey"`
	CoverageType string            `json:"coverageType"`
	Endpoint     string            `json:"endpoint"`
	Headers      map[string]string `json:"headers"`
}

type AuthResponse struct {
	ApiKey string `json:"apiKey"`
	Token  string `json:"token"`
}

type QueryResponse struct {
	QueryId string `json:"queryId"`
}

type ColumnInfo struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	DatabaseType string `json:"databaseType"`
}

type QueryStatusResponse struct {
	Status     string       `json:"status"`
	URL        string       `json:"url"`
	ColumnInfo []ColumnInfo `json:"columnInfo"`
}

type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type SQLAssistantMessage struct {
	Role      string `json:"role"`
	Context   string `json:"context"`
	AgentMode bool   `json:"agentMode"`
}

type SQLAssistantTimeRange struct {
	Hours int `json:"hours"`
}

type SQLAssistantRequest struct {
	Messages      []SQLAssistantMessage  `json:"messages"`
	TimeRange     SQLAssistantTimeRange  `json:"timeRange"`
	ExistingQuery string                 `json:"existing_query"`
}

type SQLAssistantResponse struct {
	ID            string                 `json:"id"`
	SQLQuery      string                 `json:"sql_query"`
	Context       string                 `json:"context"`
	Visualization map[string]interface{} `json:"visualization"`
}

type Client struct {
	httpClient        *http.Client
	baseURL           string
	authBaseURL       string
	apiKey            string
	token             string
	customHeaders     map[string]string
	debug             bool
	silent            bool
	format            string
	history           []string
	fromTime          int64
	toTime            int64
	multiLineBuffer   string
	inMultiLine       bool
	historyIndex      int    // Current position in history navigation
	currentInput      string // Current input being typed
	tempHistoryEntry  string // Temporary entry for current session
	profileName       string // Profile name for prompt display
}

func main() {
	var (
		profile    = flag.String("profile", "", "Soracom CLI profile to use (default: 'default')")
		sqlQuery   = flag.String("sql", "", "SQL query to execute")
		schemaOnly = flag.Bool("schema", false, "Retrieve schema information only")
		debug      = flag.Bool("debug", false, "Enable debug mode")
		openFile   = flag.Bool("open", false, "Open result file in text editor")
		fromTime   = flag.String("from", "", "Start time for query (Unix timestamp in seconds or relative time like '-24h')")
		toTime     = flag.String("to", "", "End time for query (Unix timestamp in seconds or relative time like 'now')")
		format     = flag.String("format", "table", "Output format: table, csv, json")
		silent     = flag.Bool("s", false, "Silent mode - suppress animations (default: true for piped input)")
		silentLong = flag.Bool("silent", false, "Silent mode - suppress animations (default: true for piped input)")
		help       = flag.Bool("h", false, "Show help")
	)
	flag.Parse()

	if *help {
		showHelp()
		return
	}

	profileName := *profile
	if profileName == "" {
		profileName = "default"
	}
	
	// Parse time parameters
	fromUnix, toUnix, err := parseTimeWindow(*fromTime, *toTime)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Time parsing error: %v\n", err)
		os.Exit(1)
	}

	// Validate format option
	if *format != "table" && *format != "csv" && *format != "json" {
		fmt.Fprintf(os.Stderr, "Invalid format '%s'. Supported formats: table, csv, json\n", *format)
		os.Exit(1)
	}

	// Determine silent mode - default to true for piped input or -sql mode, false for interactive
	silentMode := *silent || *silentLong || isPipedInput() || *sqlQuery != ""

	client := &Client{
		httpClient:  &http.Client{},
		debug:       *debug,
		silent:      silentMode,
		format:      *format,
		fromTime:    fromUnix,
		toTime:      toUnix,
		profileName: profileName,
	}

	if err := client.authenticate(profileName); err != nil {
		fmt.Fprintf(os.Stderr, "Authentication failed: %v\n", err)
		os.Exit(1)
	}

	if *schemaOnly {
		if err := client.getSchemas(); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to get schemas: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Determine mode: single query or interactive
	if *sqlQuery != "" {
		// Single query mode
		if err := client.executeQuery(*sqlQuery, *openFile); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to execute query: %v\n", err)
			os.Exit(1)
		}
	} else if isPipedInput() {
		// Piped input mode - process each line as a separate query
		client.runPipedMode(*openFile)
	} else {
		// Interactive mode - continuous loop
		client.runInteractiveMode(*openFile)
	}
}

func (c *Client) runInteractiveMode(openFile bool) {
	c.loadHistory()
	c.historyIndex = 0 // Initialize history navigation
	
	executor := func(input string) {
		input = strings.TrimSpace(input)
		if input == "" {
			return
		}

		// Reset history navigation when executing a command
		c.historyIndex = 0
		c.tempHistoryEntry = ""

		// Check for exit commands
		if isExitCommand(input) {
			c.saveHistory()
			os.Exit(0)
		}

		// Handle multi-line input
		if c.inMultiLine {
			// Add current line to buffer
			if c.multiLineBuffer != "" {
				c.multiLineBuffer += " " + input
			} else {
				c.multiLineBuffer = input
			}
			
			// Check if we have a complete statement (ends with semicolon)
			if strings.HasSuffix(input, ";") {
				// Execute the complete multi-line query
				completeQuery := strings.TrimSpace(c.multiLineBuffer)
				c.multiLineBuffer = ""
				c.inMultiLine = false
				
				// Add to history - store the flattened query but with a marker for multi-line
				c.addToHistory(completeQuery)
				c.saveHistory()
				
				if err := c.executeQuery(completeQuery, openFile); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				}
				return
			} else {
				// Still in multi-line mode, continue
				return
			}
		}

		// Check for special commands
		if strings.ToLower(input) == ".tables" {
			if err := c.showTables(); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}
			return
		}

		// Check for .schema command
		if strings.HasPrefix(strings.ToLower(input), ".schema") {
			parts := strings.Fields(input)
			var tableName string
			if len(parts) > 1 {
				tableName = strings.TrimRight(parts[1], ";")
			}
			if err := c.showSchema(tableName); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}
			return
		}

		// Check for .ask command (SQL assistant)
		if strings.HasPrefix(strings.ToLower(input), ".ask ") {
			context := strings.TrimSpace(input[5:]) // Remove ".ask " prefix
			if context == "" {
				fmt.Println("Usage: .ask <your question about SQL or data>")
				return
			}
			
			// Use existing query from history if available
			existingQuery := ""
			if len(c.history) > 0 {
				// Look for the last SQL query (not a command)
				for i := len(c.history) - 1; i >= 0; i-- {
					h := strings.TrimSpace(c.history[i])
					if !strings.HasPrefix(h, ".") && !isExitCommand(h) {
						existingQuery = h
						break
					}
				}
			}
			
			// Show animation while waiting for SQL assistant (unless in silent mode)
			var stopAnimation chan bool
			if !c.silent {
				stopAnimation = make(chan bool)
				go func() {
					c.showSQLAssistantAnimation(stopAnimation)
				}()
			}
			
			response, err := c.callSQLAssistant(context, existingQuery)
			
			// Stop animation
			if !c.silent {
				stopAnimation <- true
			}
			
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				return
			}
			
			// Display response without header
			if response.Context != "" {
				fmt.Printf("\n%s\n", response.Context)
			}
			if response.SQLQuery != "" {
				fmt.Printf("\nSuggested SQL:\n%s\n", response.SQLQuery)
				fmt.Printf("\n🚀 Executing query automatically...\n")
				
				// Auto-execute the suggested query (do not save to history since it's not user-typed)
				err := c.executeQuery(response.SQLQuery, openFile)
				
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error executing query: %v\n", err)
				}
			}
			fmt.Println()
			return
		}

		// Check for .window command (time window settings)
		if strings.HasPrefix(strings.ToLower(input), ".window") {
			parts := strings.Fields(input)
			if len(parts) == 1 || (len(parts) == 2 && strings.ToLower(parts[1]) == "show") {
				// Show current window settings
				c.showCurrentWindow()
			} else if len(parts) == 2 && strings.ToLower(parts[1]) == "clear" {
				// Clear window settings
				c.clearWindow()
				fmt.Println("Time window cleared.")
			} else if len(parts) == 3 {
				// Set window with from and to parameters
				fromStr := parts[1]
				toStr := parts[2]
				if err := c.setWindow(fromStr, toStr); err != nil {
					fmt.Fprintf(os.Stderr, "Error setting window: %v\n", err)
				} else {
					fmt.Printf("Time window set: from %s to %s\n", fromStr, toStr)
					c.showCurrentWindow()
				}
			} else {
				fmt.Println("Usage: .window [show|clear|<from> <to>]")
				fmt.Println("Examples:")
				fmt.Println("  .window show          # Show current time window")
				fmt.Println("  .window clear         # Clear time window")
				fmt.Println("  .window -24h now      # Set window from 24 hours ago to now")
				fmt.Println("  .window 1640995200 1641081600  # Set specific timestamps")
			}
			return
		}

		// Check for .debug command (toggle debug mode)
		if strings.HasPrefix(strings.ToLower(input), ".debug") {
			parts := strings.Fields(input)
			if len(parts) == 1 {
				// Toggle debug mode
				c.debug = !c.debug
				if c.debug {
					fmt.Println("Debug mode enabled.")
				} else {
					fmt.Println("Debug mode disabled.")
				}
			} else if len(parts) == 2 {
				switch strings.ToLower(parts[1]) {
				case "on", "true", "1":
					c.debug = true
					fmt.Println("Debug mode enabled.")
				case "off", "false", "0":
					c.debug = false
					fmt.Println("Debug mode disabled.")
				case "show", "status":
					if c.debug {
						fmt.Println("Debug mode is currently enabled.")
					} else {
						fmt.Println("Debug mode is currently disabled.")
					}
				default:
					fmt.Println("Usage: .debug [on|off|show]")
					fmt.Println("Examples:")
					fmt.Println("  .debug        # Toggle debug mode")
					fmt.Println("  .debug on     # Enable debug mode")
					fmt.Println("  .debug off    # Disable debug mode")
					fmt.Println("  .debug show   # Show current debug status")
				}
			} else {
				fmt.Println("Usage: .debug [on|off|show]")
				fmt.Println("Examples:")
				fmt.Println("  .debug        # Toggle debug mode")
				fmt.Println("  .debug on     # Enable debug mode")
				fmt.Println("  .debug off    # Disable debug mode")
				fmt.Println("  .debug show   # Show current debug status")
			}
			return
		}

		// Check for .format command (set output format)
		if strings.HasPrefix(strings.ToLower(input), ".format") {
			parts := strings.Fields(input)
			if len(parts) == 1 {
				// Show current format
				fmt.Printf("Current output format: %s\n", c.format)
			} else if len(parts) == 2 {
				newFormat := strings.ToLower(parts[1])
				switch newFormat {
				case "table", "csv", "json":
					c.format = newFormat
					fmt.Printf("Output format set to: %s\n", newFormat)
				case "show", "status":
					fmt.Printf("Current output format: %s\n", c.format)
				default:
					fmt.Println("Usage: .format [table|csv|json|show]")
					fmt.Println("Examples:")
					fmt.Println("  .format           # Show current format")
					fmt.Println("  .format table     # Set format to table")
					fmt.Println("  .format csv       # Set format to CSV")
					fmt.Println("  .format json      # Set format to JSON")
					fmt.Println("  .format show      # Show current format")
				}
			} else {
				fmt.Println("Usage: .format [table|csv|json|show]")
				fmt.Println("Examples:")
				fmt.Println("  .format           # Show current format")
				fmt.Println("  .format table     # Set format to table")
				fmt.Println("  .format csv       # Set format to CSV")
				fmt.Println("  .format json      # Set format to JSON")
				fmt.Println("  .format show      # Show current format")
			}
			return
		}

		// Check if this is an incomplete SQL statement (doesn't end with semicolon)
		if !strings.HasSuffix(input, ";") {
			// Enter multi-line mode
			c.inMultiLine = true
			c.multiLineBuffer = input
			return
		}

		// Complete single-line query - execute immediately
		c.addToHistory(input)
		c.saveHistory()

		if err := c.executeQuery(input, openFile); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
	}

	completer := func(d prompt.Document) []prompt.Suggest {
		return c.getCompletions(d)
	}

	// Dynamic prefix function for multi-line support
	prefixFunc := func() (string, bool) {
		if c.inMultiLine {
			return "   ...> ", true
		}
		return c.profileName + "> ", true
	}

	// Custom key bind for history navigation
	keyBindings := []prompt.KeyBind{
		{
			Key: prompt.Up, // Up Arrow
			Fn: func(buf *prompt.Buffer) {
				c.navigateHistory(-1, buf)
			},
		},
		{
			Key: prompt.Down, // Down Arrow
			Fn: func(buf *prompt.Buffer) {
				c.navigateHistory(1, buf)
			},
		},
	}

	p := prompt.New(
		executor,
		completer,
		prompt.OptionPrefix(c.profileName+"> "),
		prompt.OptionLivePrefix(prefixFunc),
		prompt.OptionTitle("SoraQL Interactive SQL Client"),
		prompt.OptionMaxSuggestion(10),
		prompt.OptionCompletionOnDown(),
		prompt.OptionAddKeyBind(keyBindings...),
	)

	p.Run()
}

func (c *Client) getInteractiveQuery(scanner *bufio.Scanner) string {
	var lines []string
	firstLine := strings.TrimSpace(scanner.Text())
	
	if firstLine == "" {
		return ""
	}
	
	lines = append(lines, firstLine)
	
	// Continue reading if the line doesn't end with semicolon
	currentLine := firstLine
	for !strings.HasSuffix(currentLine, ";") {
		fmt.Print("   ...> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			break
		}
		lines = append(lines, line)
		currentLine = line
	}
	
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Error reading input: %v\n", err)
		return ""
	}
	
	query := strings.Join(lines, " ")
	// Remove trailing semicolon if present
	query = strings.TrimSuffix(query, ";")
	return strings.TrimSpace(query)
}

func isPipedInput() bool {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	// Check if stdin is being piped to (not a terminal)
	return (stat.Mode() & os.ModeCharDevice) == 0
}

func (c *Client) runPipedMode(openFile bool) {
	scanner := bufio.NewScanner(os.Stdin)
	
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		
		// Check for exit commands
		if isExitCommand(line) {
			break
		}
		
		// Check for special commands
		if strings.ToLower(strings.TrimSpace(line)) == ".tables" {
			if err := c.showTables(); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}
			continue
		}

		// Check for .schema command
		trimmedLine := strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(trimmedLine), ".schema") {
			parts := strings.Fields(trimmedLine)
			var tableName string
			if len(parts) > 1 {
				tableName = strings.TrimRight(parts[1], ";")
			}
			if err := c.showSchema(tableName); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}
			continue
		}

		// Check for .window command (time window settings)
		if strings.HasPrefix(strings.ToLower(trimmedLine), ".window") {
			parts := strings.Fields(trimmedLine)
			if len(parts) == 1 || (len(parts) == 2 && strings.ToLower(parts[1]) == "show") {
				// Show current window settings
				c.showCurrentWindow()
			} else if len(parts) == 2 && strings.ToLower(parts[1]) == "clear" {
				// Clear window settings
				c.clearWindow()
				fmt.Println("Time window cleared.")
			} else if len(parts) == 3 {
				// Set window with from and to parameters
				fromStr := parts[1]
				toStr := parts[2]
				if err := c.setWindow(fromStr, toStr); err != nil {
					fmt.Fprintf(os.Stderr, "Error setting window: %v\n", err)
				} else {
					fmt.Printf("Time window set: from %s to %s\n", fromStr, toStr)
					c.showCurrentWindow()
				}
			} else {
				fmt.Println("Usage: .window [show|clear|<from> <to>]")
				fmt.Println("Examples:")
				fmt.Println("  .window show          # Show current time window")
				fmt.Println("  .window clear         # Clear time window")
				fmt.Println("  .window -24h now      # Set window from 24 hours ago to now")
				fmt.Println("  .window 1640995200 1641081600  # Set specific timestamps")
			}
			continue
		}

		// Check for .debug command (toggle debug mode)
		if strings.HasPrefix(strings.ToLower(trimmedLine), ".debug") {
			parts := strings.Fields(trimmedLine)
			if len(parts) == 1 {
				// Toggle debug mode
				c.debug = !c.debug
				if c.debug {
					fmt.Println("Debug mode enabled.")
				} else {
					fmt.Println("Debug mode disabled.")
				}
			} else if len(parts) == 2 {
				switch strings.ToLower(parts[1]) {
				case "on", "true", "1":
					c.debug = true
					fmt.Println("Debug mode enabled.")
				case "off", "false", "0":
					c.debug = false
					fmt.Println("Debug mode disabled.")
				case "show", "status":
					if c.debug {
						fmt.Println("Debug mode is currently enabled.")
					} else {
						fmt.Println("Debug mode is currently disabled.")
					}
				default:
					fmt.Println("Usage: .debug [on|off|show]")
					fmt.Println("Examples:")
					fmt.Println("  .debug        # Toggle debug mode")
					fmt.Println("  .debug on     # Enable debug mode")
					fmt.Println("  .debug off    # Disable debug mode")
					fmt.Println("  .debug show   # Show current debug status")
				}
			} else {
				fmt.Println("Usage: .debug [on|off|show]")
				fmt.Println("Examples:")
				fmt.Println("  .debug        # Toggle debug mode")
				fmt.Println("  .debug on     # Enable debug mode")
				fmt.Println("  .debug off    # Disable debug mode")
				fmt.Println("  .debug show   # Show current debug status")
			}
			continue
		}

		// Check for .format command (set output format)
		if strings.HasPrefix(strings.ToLower(trimmedLine), ".format") {
			parts := strings.Fields(trimmedLine)
			if len(parts) == 1 {
				// Show current format
				fmt.Printf("Current output format: %s\n", c.format)
			} else if len(parts) == 2 {
				newFormat := strings.ToLower(parts[1])
				switch newFormat {
				case "table", "csv", "json":
					c.format = newFormat
					fmt.Printf("Output format set to: %s\n", newFormat)
				case "show", "status":
					fmt.Printf("Current output format: %s\n", c.format)
				default:
					fmt.Println("Usage: .format [table|csv|json|show]")
					fmt.Println("Examples:")
					fmt.Println("  .format           # Show current format")
					fmt.Println("  .format table     # Set format to table")
					fmt.Println("  .format csv       # Set format to CSV")
					fmt.Println("  .format json      # Set format to JSON")
					fmt.Println("  .format show      # Show current format")
				}
			} else {
				fmt.Println("Usage: .format [table|csv|json|show]")
				fmt.Println("Examples:")
				fmt.Println("  .format           # Show current format")
				fmt.Println("  .format table     # Set format to table")
				fmt.Println("  .format csv       # Set format to CSV")
				fmt.Println("  .format json      # Set format to JSON")
				fmt.Println("  .format show      # Show current format")
			}
			continue
		}
		
		// Remove trailing semicolon if present
		query := strings.TrimSuffix(line, ";")
		query = strings.TrimSpace(query)
		
		if query != "" {
			if err := c.executeQuery(query, openFile); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				// Don't exit, continue to next query
			}
		}
	}
	
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Error reading input: %v\n", err)
	}
}

func (c *Client) getHistoryFile() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ".soraql_history"
	}
	return filepath.Join(homeDir, ".soraql_history")
}

func (c *Client) getCompletions(d prompt.Document) []prompt.Suggest {
	// Only show completions if there's at least one character before cursor
	word := d.GetWordBeforeCursor()
	if word == "" {
		return []prompt.Suggest{}
	}

	// SQL keywords and table names
	suggestions := []prompt.Suggest{
		// Special Commands
		{Text: ".tables", Description: "Show all available tables"},
		{Text: ".schema", Description: "Show table schema (.schema TABLE_NAME)"},
		{Text: ".ask", Description: "Ask SQL assistant for help (.ask your question)"},
		{Text: ".window", Description: "Set time window (.window show|clear|<from> <to>)"},
		{Text: ".debug", Description: "Toggle debug mode (.debug on|off|show)"},
		{Text: ".format", Description: "Set output format (.format table|csv|json|show)"},
		
		// SQL Keywords
		{Text: "SELECT", Description: "Select data from table"},
		{Text: "FROM", Description: "Specify table source"},
		{Text: "WHERE", Description: "Filter condition"},
		{Text: "ORDER BY", Description: "Sort results"},
		{Text: "GROUP BY", Description: "Group results"},
		{Text: "HAVING", Description: "Filter grouped results"},
		{Text: "LIMIT", Description: "Limit number of results"},
		{Text: "COUNT", Description: "Count rows"},
		{Text: "SUM", Description: "Sum values"},
		{Text: "AVG", Description: "Average values"},
		{Text: "MIN", Description: "Minimum value"},
		{Text: "MAX", Description: "Maximum value"},
		{Text: "DISTINCT", Description: "Unique values only"},
		{Text: "AS", Description: "Alias for column/table"},
		{Text: "AND", Description: "Logical AND"},
		{Text: "OR", Description: "Logical OR"},
		{Text: "NOT", Description: "Logical NOT"},
		{Text: "DESC", Description: "Descending order"},
		{Text: "ASC", Description: "Ascending order"},
		
		// Table Names
		{Text: "SIM_SNAPSHOTS", Description: "SIM card snapshots table"},
		{Text: "SIM_SESSION_EVENTS", Description: "SIM session events table"},
		{Text: "CELL_TOWERS", Description: "Cell tower information table"},
		{Text: "COUNTRIES", Description: "Country information table"},
		{Text: "HARVEST_DATA", Description: "Harvested data table"},
		{Text: "HARVEST_FILES", Description: "Harvested files table"},
		{Text: "MCC_MNC", Description: "Mobile country/network codes table"},
		{Text: "MCC", Description: "Mobile country codes table"},
		{Text: "SIM_STATS", Description: "SIM statistics table"},
		{Text: "NETWORKS", Description: "Network information table"},
		{Text: "BILLING_HISTORY", Description: "Billing history table"},
	}

	return prompt.FilterHasPrefix(suggestions, word, true)
}

func (c *Client) loadHistory() {
	historyFile := c.getHistoryFile()
	if data, err := os.ReadFile(historyFile); err == nil {
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" {
				c.history = append(c.history, line)
			}
		}
	}
}

func (c *Client) addToHistory(query string) {
	query = strings.TrimSpace(query)
	if query != "" {
		c.history = append(c.history, query)
	}
}

func (c *Client) saveHistory() {
	historyFile := c.getHistoryFile()
	content := strings.Join(c.history, "\n")
	os.WriteFile(historyFile, []byte(content), 0644)
}

func isExitCommand(query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	return query == "exit" || query == "quit" || query == "\\q" || query == ".exit" || query == ".quit"
}

func (c *Client) navigateHistory(direction int, buf *prompt.Buffer) {
	if len(c.history) == 0 {
		return
	}
	
	// Save current input if we're starting history navigation
	if c.historyIndex == 0 {
		c.tempHistoryEntry = buf.Text()
	}
	
	// Navigate history
	c.historyIndex += direction
	
	// Clamp history index to valid range
	if c.historyIndex < 0 {
		c.historyIndex = 0
		return
	}
	if c.historyIndex > len(c.history) {
		c.historyIndex = len(c.history)
	}
	
	// Get the appropriate history entry or temp entry
	var historyEntry string
	if c.historyIndex == 0 {
		historyEntry = c.tempHistoryEntry
	} else {
		historyEntry = c.history[len(c.history)-c.historyIndex]
	}
	
	// Replace buffer content with history entry
	buf.DeleteBeforeCursor(buf.DisplayCursorPosition())
	buf.InsertText(historyEntry, false, true)
}


func getInteractiveInput() string {
	// This function is now only used for non-continuous mode
	// Keeping for backward compatibility with tests
	fmt.Print("default> ")
	scanner := bufio.NewScanner(os.Stdin)
	var lines []string
	
	for {
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			break
		}
		lines = append(lines, line)
		
		// Continue reading if the line doesn't end with semicolon
		if !strings.HasSuffix(line, ";") {
			fmt.Print("   ...> ")
		} else {
			break
		}
	}
	
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Error reading input: %v\n", err)
		return ""
	}
	
	query := strings.Join(lines, " ")
	// Remove trailing semicolon if present
	query = strings.TrimSuffix(query, ";")
	return strings.TrimSpace(query)
}

func readFromStdin() string {
	// Check if there's data available on stdin
	stat, err := os.Stdin.Stat()
	if err != nil {
		return ""
	}

	// Check if stdin is being piped to (not a terminal)
	if (stat.Mode() & os.ModeCharDevice) == 0 {
		scanner := bufio.NewScanner(os.Stdin)
		var lines []string
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			return ""
		}
		return strings.TrimSpace(strings.Join(lines, " "))
	}
	
	return ""
}

func showHelp() {
	fmt.Println("Usage: soraql [options]")
	fmt.Println("       echo 'SQL_QUERY' | soraql [options]")
	fmt.Println("       soraql [options]  (starts interactive mode)")
	fmt.Println("")
	fmt.Println("Authentication options:")
	fmt.Println("  -profile PROFILE: Specify Soracom CLI profile to use (default: 'default')")
	fmt.Println("                    The profile determines coverage area (JP/Global) and endpoint")
	fmt.Println("")
	fmt.Println("Query options:")
	fmt.Println("  -sql \"QUERY\": Execute custom SQL query")
	fmt.Println("  -schema: Retrieve and display schema information")
	fmt.Println("  -from TIME: Start time for query (Unix timestamp, relative time like '-24h', or datetime)")
	fmt.Println("  -to TIME: End time for query (Unix timestamp, relative time like 'now', or datetime)")
	fmt.Println("  -format FORMAT: Output format - table, csv, json (default: table)")
	fmt.Println("  -s, -silent: Silent mode - suppress animations (default: true for piped input and -sql mode)")
	fmt.Println("  -debug: Show debug messages (authentication details, HTTP requests, etc.)")
	fmt.Println("  -open: Open downloaded result file in text editor")
	fmt.Println("")
	fmt.Println("Examples:")
	fmt.Println("  soraql -sql \"select count(*) from SIM_SNAPSHOTS\"")
	fmt.Println("  soraql -profile myprofile -sql \"select count(*) from CELL_TOWERS\"")
	fmt.Println("  soraql -schema")
	fmt.Println("  soraql -debug -sql \"select count(*) from SIM_SNAPSHOTS\"")
	fmt.Println("  soraql -open -sql \"select * from SIM_SESSION_EVENTS limit 5\"")
	fmt.Println("")
	fmt.Println("Time window examples:")
	fmt.Println("  soraql -from '-24h' -to 'now' -sql \"select * from SIM_SESSION_EVENTS\"")
	fmt.Println("  soraql -from '1640995200' -to '1641081600' -sql \"select * from SIM_SNAPSHOTS\"")
	fmt.Println("  soraql -from '2024-01-01 00:00:00' -to '2024-01-02 00:00:00' -sql \"select * from CELL_TOWERS\"")
	fmt.Println("  soraql -from '-1w' -sql \"select * from SIM_SESSION_EVENTS\" # Last week to now")
	fmt.Println("")
	fmt.Println("Format examples:")
	fmt.Println("  soraql -format csv -sql \"select * from SIM_SNAPSHOTS limit 5\"")
	fmt.Println("  soraql -format json -sql \"select * from CELL_TOWERS limit 3\"")
	fmt.Println("  soraql -format table -sql \"select count(*) from SIM_SESSION_EVENTS\"")
	fmt.Println("")
	fmt.Println("Interactive mode:")
	fmt.Println("  soraql                                    # Start interactive mode with default profile")
	fmt.Println("  soraql -profile myprofile                 # Start with specific profile")
	fmt.Println("  default> .tables                          # Show available tables")
	fmt.Println("  default> .schema SIM_SNAPSHOTS            # Show table schema")
	fmt.Println("  default> select count(*) from SIM_SNAPSHOTS;")
	fmt.Println("  default> select count(*) from CELL_TOWERS;")
	fmt.Println("  default> select                           # Multi-line query starts")
	fmt.Println("   ...>   count(*)")
	fmt.Println("   ...>   from SIM_SNAPSHOTS")
	fmt.Println("   ...>   where ICCID like '123%';          # Ends with semicolon")
	fmt.Println("  default> exit")
	fmt.Println("")
	fmt.Println("Interactive features:")
	fmt.Println("  • Command history (↑/↓ arrow keys)")
	fmt.Println("  • Inline editing (←/→ arrow keys, backspace, etc.)")
	fmt.Println("  • Multi-line query editing (continues until semicolon)")
	fmt.Println("  • Tab completion with descriptions for SQL keywords and table names")
	fmt.Println("  • Profile name shown in prompt (e.g., 'myprofile>', 'default>')")
	fmt.Println("  • History persistence (~/.soraql_history)")
	fmt.Println("")
	fmt.Println("Special commands:")
	fmt.Println("  .tables                                   # Show all available tables")
	fmt.Println("  .schema [TABLE_NAME]                      # Show table schema (all if no name)")
	fmt.Println("  .window [show|clear|<from> <to>]          # Manage time window for queries")
	fmt.Println("    .window show                            # Show current time window")
	fmt.Println("    .window clear                           # Clear time window")
	fmt.Println("    .window -24h now                        # Set window from 24 hours ago to now")
	fmt.Println("  .debug [on|off|show]                      # Toggle debug mode")
	fmt.Println("    .debug                                  # Toggle debug mode on/off")
	fmt.Println("    .debug on                               # Enable debug mode")
	fmt.Println("    .debug off                              # Disable debug mode")
	fmt.Println("    .debug show                             # Show current debug status")
	fmt.Println("  .format [table|csv|json|show]             # Set output format")
	fmt.Println("    .format                                 # Show current format")
	fmt.Println("    .format table                           # Set format to table")
	fmt.Println("    .format csv                             # Set format to CSV")
	fmt.Println("    .format json                            # Set format to JSON")
	fmt.Println("    .format show                            # Show current format")
	fmt.Println("")
	fmt.Println("Piped input mode:")
	fmt.Println("  echo 'select count(*) from SIM_SNAPSHOTS' | soraql")
	fmt.Println("  printf \"query1\\nquery2\\nexit\\n\" | soraql -profile myprofile")
	fmt.Println("")
	fmt.Println("Profile Configuration:")
	fmt.Println("  Profiles are stored in ~/.soracom/PROFILE.json and contain:")
	fmt.Println("  • Authentication credentials (email/password or authKeyId/authKey)")
	fmt.Println("  • Coverage type ('jp' for Japan, 'g' for Global)")
	fmt.Println("  • Optional custom endpoint URL")
	fmt.Println("")
	fmt.Println("Exit commands: exit, quit, \\q, .exit, .quit")
}

// parseTimeWindow parses from and to time parameters
func parseTimeWindow(fromStr, toStr string) (int64, int64, error) {
	var fromTime, toTime int64
	var err error

	// Parse from time
	if fromStr != "" {
		fromTime, err = parseTimeParam(fromStr)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid from time '%s': %v", fromStr, err)
		}
	}

	// Parse to time
	if toStr != "" {
		toTime, err = parseTimeParam(toStr)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid to time '%s': %v", toStr, err)
		}
	}

	// Validate time window
	if fromTime > 0 && toTime > 0 && fromTime >= toTime {
		return 0, 0, fmt.Errorf("from time (%d) must be before to time (%d)", fromTime, toTime)
	}

	return fromTime, toTime, nil
}

// parseTimeParam parses a single time parameter
func parseTimeParam(timeStr string) (int64, error) {
	// Handle "now" keyword
	if strings.ToLower(timeStr) == "now" {
		return time.Now().Unix(), nil
	}

	// Try to parse as Unix timestamp
	if timestamp, err := strconv.ParseInt(timeStr, 10, 64); err == nil {
		// Validate reasonable timestamp range (after 2000-01-01 and before 2100-01-01)
		if timestamp > 946684800 && timestamp < 4102444800 {
			return timestamp, nil
		}
		return 0, fmt.Errorf("timestamp %d is out of reasonable range", timestamp)
	}

	// Handle relative time (e.g., "-24h", "-1d", "-1w")
	if strings.HasPrefix(timeStr, "-") {
		duration, err := parseRelativeTime(timeStr[1:]) // Remove the '-' prefix
		if err != nil {
			return 0, err
		}
		return time.Now().Add(-duration).Unix(), nil
	}

	// Try to parse as RFC3339 datetime
	if t, err := time.Parse(time.RFC3339, timeStr); err == nil {
		return t.Unix(), nil
	}

	// Try to parse as common date formats
	formats := []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
	}

	for _, format := range formats {
		if t, err := time.Parse(format, timeStr); err == nil {
			return t.Unix(), nil
		}
	}

	return 0, fmt.Errorf("unable to parse time format")
}

// showCurrentWindow displays the current time window settings
func (c *Client) showCurrentWindow() {
	if c.fromTime == 0 && c.toTime == 0 {
		fmt.Println("No time window set (queries will use default time range)")
		return
	}
	
	fmt.Println("Current time window:")
	if c.fromTime > 0 {
		fmt.Printf("  From: %d (%s)\n", c.fromTime, time.Unix(c.fromTime, 0).Format(time.RFC3339))
	} else {
		fmt.Println("  From: (not set)")
	}
	
	if c.toTime > 0 {
		fmt.Printf("  To:   %d (%s)\n", c.toTime, time.Unix(c.toTime, 0).Format(time.RFC3339))
	} else {
		fmt.Println("  To:   (not set)")
	}
}

// clearWindow clears the current time window settings
func (c *Client) clearWindow() {
	c.fromTime = 0
	c.toTime = 0
}

// setWindow sets the time window with validation
func (c *Client) setWindow(fromStr, toStr string) error {
	fromTime, toTime, err := parseTimeWindow(fromStr, toStr)
	if err != nil {
		return err
	}
	
	c.fromTime = fromTime
	c.toTime = toTime
	return nil
}

// parseRelativeTime parses relative time strings like "24h", "1d", "1w"
func parseRelativeTime(relativeStr string) (time.Duration, error) {
	if len(relativeStr) < 2 {
		return 0, fmt.Errorf("invalid relative time format")
	}

	unit := relativeStr[len(relativeStr)-1:]
	valueStr := relativeStr[:len(relativeStr)-1]

	value, err := strconv.Atoi(valueStr)
	if err != nil {
		return 0, fmt.Errorf("invalid numeric value in relative time")
	}

	switch unit {
	case "s":
		return time.Duration(value) * time.Second, nil
	case "m":
		return time.Duration(value) * time.Minute, nil
	case "h":
		return time.Duration(value) * time.Hour, nil
	case "d":
		return time.Duration(value) * 24 * time.Hour, nil
	case "w":
		return time.Duration(value) * 7 * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("unsupported time unit '%s' (supported: s, m, h, d, w)", unit)
	}
}


func (c *Client) authenticate(profile string) error {
	configPath := filepath.Join(os.Getenv("HOME"), ".soracom", profile+".json")
	
	if c.debug {
		fmt.Printf("Using profile: %s\n", profile)
		fmt.Printf("Config path: %s\n", configPath)
	}

	configFile, err := os.Open(configPath)
	if err != nil {
		return fmt.Errorf("config file '%s' not found: %v", configPath, err)
	}
	defer configFile.Close()

	var config Config
	if err := json.NewDecoder(configFile).Decode(&config); err != nil {
		return fmt.Errorf("failed to parse config file: %v", err)
	}

	// Set base URLs based on profile configuration
	if config.Endpoint != "" {
		// Use endpoint from profile if specified (remove https:// prefix if present)
		endpoint := strings.TrimPrefix(config.Endpoint, "https://")
		endpoint = strings.TrimPrefix(endpoint, "http://")
		c.baseURL = endpoint
		c.authBaseURL = endpoint
	} else {
		// Default to production endpoints based on coverage type
		if config.CoverageType == "g" {
			c.baseURL = "g.api.soracom.io"
			c.authBaseURL = "g.api.soracom.io"
		} else {
			// Default to JP coverage
			c.baseURL = "jp.api.soracom.io"
			c.authBaseURL = "jp.api.soracom.io"
		}
	}

	// Store custom headers from profile
	c.customHeaders = config.Headers
	if c.customHeaders == nil {
		c.customHeaders = make(map[string]string)
	}

	if c.debug {
		fmt.Printf("Coverage Type: %s\n", config.CoverageType)
		fmt.Printf("Endpoint: %s\n", config.Endpoint)
		fmt.Printf("Base URL: %s\n", c.baseURL)
		fmt.Printf("Auth Base URL: %s\n", c.authBaseURL)
		if len(c.customHeaders) > 0 {
			fmt.Printf("Custom Headers: %v\n", c.customHeaders)
		}
		fmt.Printf("Email: %s\n", config.Email)
		fmt.Printf("Password: %s\n", config.Password)
		fmt.Printf("API Key ID: %s\n", config.AuthKeyId)
		fmt.Printf("Auth Key: %s\n", config.AuthKey)
	}

	var authPayload interface{}
	if config.Email != "" && config.Password != "" {
		authPayload = map[string]string{
			"email":    config.Email,
			"password": config.Password,
		}
	} else {
		authPayload = map[string]string{
			"authKeyId": config.AuthKeyId,
			"authKey":   config.AuthKey,
		}
	}

	payloadBytes, err := json.Marshal(authPayload)
	if err != nil {
		return fmt.Errorf("failed to marshal auth payload: %v", err)
	}

	req, err := http.NewRequest("POST", fmt.Sprintf("https://%s/v1/auth", c.authBaseURL), bytes.NewBuffer(payloadBytes))
	if err != nil {
		return fmt.Errorf("failed to create auth request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	
	// Add custom headers from profile
	for key, value := range c.customHeaders {
		req.Header.Set(key, value)
	}

	if c.debug {
		fmt.Printf("Auth URL: %s\n", req.URL.String())
		fmt.Printf("Auth payload: %s\n", string(payloadBytes))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("auth request failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read auth response: %v", err)
	}

	if c.debug {
		fmt.Printf("Auth response: %s\n", string(body))
	}

	var authResp AuthResponse
	if err := json.Unmarshal(body, &authResp); err != nil {
		return fmt.Errorf("failed to parse auth response: %v", err)
	}

	c.apiKey = authResp.ApiKey
	c.token = authResp.Token

	return nil
}

func (c *Client) makeRequest(method, url string, payload interface{}) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		payloadBytes, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal payload: %v", err)
		}
		body = bytes.NewBuffer(payloadBytes)
		if c.debug {
			fmt.Printf("Request payload: %s\n", string(payloadBytes))
		}
	}

	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("x-soracom-api-key", c.apiKey)
	req.Header.Set("x-soracom-token", c.token)
	
	// Add custom headers from profile
	for key, value := range c.customHeaders {
		req.Header.Set(key, value)
	}
	
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if c.debug {
		fmt.Printf("Request URL: %s\n", req.URL.String())
		fmt.Printf("Request method: %s\n", method)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %v", err)
	}

	if c.debug {
		fmt.Printf("Response status: %d\n", resp.StatusCode)
		fmt.Printf("Response body: %s\n", string(responseBody))
	}

	// Check for HTTP error status codes
	if resp.StatusCode >= 400 {
		var errorResp ErrorResponse
		if err := json.Unmarshal(responseBody, &errorResp); err == nil && errorResp.Code != "" {
			return nil, fmt.Errorf("API error [%s]: %s", errorResp.Code, errorResp.Message)
		}
		return nil, fmt.Errorf("HTTP %d error: %s", resp.StatusCode, string(responseBody))
	}

	return responseBody, nil
}

func (c *Client) getSchemas() error {
	body, err := c.makeRequest("GET", fmt.Sprintf("https://%s/v1/analysis/schemas", c.baseURL), nil)
	if err != nil {
		return err
	}

	var prettyJSON bytes.Buffer
	if err := json.Indent(&prettyJSON, body, "", "  "); err != nil {
		fmt.Println(string(body))
	} else {
		fmt.Println(prettyJSON.String())
	}

	return nil
}

func (c *Client) showTables() error {
	body, err := c.makeRequest("GET", fmt.Sprintf("https://%s/v1/analysis/schemas", c.baseURL), nil)
	if err != nil {
		return err
	}

	// Parse the schema response to extract tables
	var schema map[string]interface{}
	if err := json.Unmarshal(body, &schema); err != nil {
		return fmt.Errorf("failed to parse schema response: %v", err)
	}

	// Extract table names from schema
	tableNames := c.extractTableNames(schema)
	
	if len(tableNames) == 0 {
		fmt.Println("No tables found.")
		return nil
	}

	// Display tables in a simple list format
	fmt.Println("Tables:")
	fmt.Println("┌────────────────────────────────────────────┐")
	for _, tableName := range tableNames {
		fmt.Printf("│ %-42s │\n", tableName)
	}
	fmt.Println("└────────────────────────────────────────────┘")
	fmt.Printf("\n(%d tables)\n", len(tableNames))

	return nil
}

func (c *Client) extractTableNames(schema map[string]interface{}) []string {
	var tableNames []string
	
	// Try to extract tables from different possible schema structures
	// This is a generic approach that handles various schema formats
	if tables, exists := schema["tables"]; exists {
		if tableList, ok := tables.([]interface{}); ok {
			for _, table := range tableList {
				if tableMap, ok := table.(map[string]interface{}); ok {
					if name, exists := tableMap["name"]; exists {
						if nameStr, ok := name.(string); ok {
							tableNames = append(tableNames, nameStr)
						}
					}
				}
			}
		}
	}
	
	// If no tables found in expected structure, try alternative approaches
	if len(tableNames) == 0 {
		// Look for any keys that might represent table names
		for key, value := range schema {
			if key == "schemas" || key == "databases" {
				if schemaMap, ok := value.(map[string]interface{}); ok {
					for _, schemaValue := range schemaMap {
						if schemaData, ok := schemaValue.(map[string]interface{}); ok {
							if tables, exists := schemaData["tables"]; exists {
								if tableMap, ok := tables.(map[string]interface{}); ok {
									for tableName := range tableMap {
										tableNames = append(tableNames, tableName)
									}
								}
							}
						}
					}
				}
			}
		}
	}
	
	// Sort table names for consistent display
	if len(tableNames) > 1 {
		for i := 0; i < len(tableNames)-1; i++ {
			for j := i + 1; j < len(tableNames); j++ {
				if tableNames[i] > tableNames[j] {
					tableNames[i], tableNames[j] = tableNames[j], tableNames[i]
				}
			}
		}
	}
	
	return tableNames
}

func (c *Client) showSchema(tableName string) error {
	body, err := c.makeRequest("GET", fmt.Sprintf("https://%s/v1/analysis/schemas", c.baseURL), nil)
	if err != nil {
		return err
	}

	// Parse the schema response
	var schema map[string]interface{}
	if err := json.Unmarshal(body, &schema); err != nil {
		return fmt.Errorf("failed to parse schema response: %v", err)
	}

	// Debug: show raw schema if no table schemas found
	if c.debug {
		fmt.Printf("Raw schema response: %s\n", string(body))
	}

	if tableName == "" {
		// Show all schemas
		return c.displayAllSchemas(schema)
	} else {
		// Show specific table schema
		return c.displayTableSchema(schema, tableName)
	}
}

func (c *Client) displayAllSchemas(schema map[string]interface{}) error {
	tableSchemas := c.extractAllTableSchemas(schema)
	
	if len(tableSchemas) == 0 {
		fmt.Println("No table schemas found in expected format.")
		fmt.Println("Raw schema structure:")
		
		// Show a pretty-printed version of the raw schema
		var prettyJSON bytes.Buffer
		if err := json.Indent(&prettyJSON, nil, "", "  "); err == nil {
			// Re-marshal the parsed schema for pretty printing
			if rawBytes, err := json.MarshalIndent(schema, "", "  "); err == nil {
				fmt.Println(string(rawBytes))
			}
		} else {
			// Fallback to original format
			fmt.Printf("%+v\n", schema)
		}
		return nil
	}

	fmt.Println("Table Schemas:")
	fmt.Println()

	for tableName, columns := range tableSchemas {
		fmt.Printf("Table: %s\n", tableName)
		
		// Check if any columns have descriptions
		hasDescriptions := false
		for _, col := range columns {
			if col.Description != "" {
				hasDescriptions = true
				break
			}
		}
		
		// Calculate dynamic column widths
		maxColNameWidth := len("Column")
		maxTypeWidth := len("Type")
		
		for _, col := range columns {
			if len(col.Name) > maxColNameWidth {
				maxColNameWidth = len(col.Name)
			}
			if len(col.Type) > maxTypeWidth {
				maxTypeWidth = len(col.Type)
			}
		}
		
		if hasDescriptions {
			// Show 3-column layout with descriptions
			descWidth := 40 // Slightly smaller for multi-table view
			
			// Print top border
			fmt.Print("┌")
			fmt.Print(strings.Repeat("─", maxColNameWidth+2))
			fmt.Print("┬")
			fmt.Print(strings.Repeat("─", maxTypeWidth+2))
			fmt.Print("┬")
			fmt.Print(strings.Repeat("─", descWidth+2))
			fmt.Println("┐")
			
			// Print header
			fmt.Printf("│ %-*s │ %-*s │ %-*s │\n", maxColNameWidth, "Column", maxTypeWidth, "Type", descWidth, "Description")
			
			// Print separator
			fmt.Print("├")
			fmt.Print(strings.Repeat("─", maxColNameWidth+2))
			fmt.Print("┼")
			fmt.Print(strings.Repeat("─", maxTypeWidth+2))
			fmt.Print("┼")
			fmt.Print(strings.Repeat("─", descWidth+2))
			fmt.Println("┤")
			
			// Print data rows
			for _, col := range columns {
				desc := col.Description
				if desc == "" {
					desc = "-"
				}
				// Truncate long descriptions to fit
				if len(desc) > descWidth {
					desc = desc[:descWidth-3] + "..."
				}
				fmt.Printf("│ %-*s │ %-*s │ %-*s │\n", maxColNameWidth, col.Name, maxTypeWidth, col.Type, descWidth, desc)
			}
			
			// Print bottom border
			fmt.Print("└")
			fmt.Print(strings.Repeat("─", maxColNameWidth+2))
			fmt.Print("┴")
			fmt.Print(strings.Repeat("─", maxTypeWidth+2))
			fmt.Print("┴")
			fmt.Print(strings.Repeat("─", descWidth+2))
			fmt.Println("┘")
		} else {
			// Show 2-column layout without descriptions
			// Print top border
			fmt.Print("┌")
			fmt.Print(strings.Repeat("─", maxColNameWidth+2))
			fmt.Print("┬")
			fmt.Print(strings.Repeat("─", maxTypeWidth+2))
			fmt.Println("┐")
			
			// Print header
			fmt.Printf("│ %-*s │ %-*s │\n", maxColNameWidth, "Column", maxTypeWidth, "Type")
			
			// Print separator
			fmt.Print("├")
			fmt.Print(strings.Repeat("─", maxColNameWidth+2))
			fmt.Print("┼")
			fmt.Print(strings.Repeat("─", maxTypeWidth+2))
			fmt.Println("┤")
			
			// Print data rows
			for _, col := range columns {
				fmt.Printf("│ %-*s │ %-*s │\n", maxColNameWidth, col.Name, maxTypeWidth, col.Type)
			}
			
			// Print bottom border
			fmt.Print("└")
			fmt.Print(strings.Repeat("─", maxColNameWidth+2))
			fmt.Print("┴")
			fmt.Print(strings.Repeat("─", maxTypeWidth+2))
			fmt.Println("┘")
		}
		
		fmt.Printf("(%d columns)\n\n", len(columns))
	}

	return nil
}

func (c *Client) displayTableSchema(schema map[string]interface{}, tableName string) error {
	tableSchemas := c.extractAllTableSchemas(schema)
	
	columns, exists := tableSchemas[tableName]
	if !exists {
		// Try case-insensitive search
		for name, cols := range tableSchemas {
			if strings.EqualFold(name, tableName) {
				columns = cols
				tableName = name // Use the actual table name
				exists = true
				break
			}
		}
	}
	
	if !exists {
		return fmt.Errorf("table '%s' not found", tableName)
	}

	fmt.Printf("Schema for table: %s\n", tableName)
	
	// Check if any columns have descriptions
	hasDescriptions := false
	for _, col := range columns {
		if col.Description != "" {
			hasDescriptions = true
			break
		}
	}
	
	// Calculate dynamic column widths
	maxColNameWidth := len("Column")
	maxTypeWidth := len("Type")
	
	for _, col := range columns {
		if len(col.Name) > maxColNameWidth {
			maxColNameWidth = len(col.Name)
		}
		if len(col.Type) > maxTypeWidth {
			maxTypeWidth = len(col.Type)
		}
	}
	
	if hasDescriptions {
		// Show 3-column layout with descriptions
		descWidth := 50 // Fixed description width
		
		// Print top border
		fmt.Print("┌")
		fmt.Print(strings.Repeat("─", maxColNameWidth+2))
		fmt.Print("┬")
		fmt.Print(strings.Repeat("─", maxTypeWidth+2))
		fmt.Print("┬")
		fmt.Print(strings.Repeat("─", descWidth+2))
		fmt.Println("┐")
		
		// Print header
		fmt.Printf("│ %-*s │ %-*s │ %-*s │\n", maxColNameWidth, "Column", maxTypeWidth, "Type", descWidth, "Description")
		
		// Print separator
		fmt.Print("├")
		fmt.Print(strings.Repeat("─", maxColNameWidth+2))
		fmt.Print("┼")
		fmt.Print(strings.Repeat("─", maxTypeWidth+2))
		fmt.Print("┼")
		fmt.Print(strings.Repeat("─", descWidth+2))
		fmt.Println("┤")
		
		// Print data rows
		for _, col := range columns {
			desc := col.Description
			if desc == "" {
				desc = "-"
			}
			// Truncate long descriptions to fit
			if len(desc) > descWidth {
				desc = desc[:descWidth-3] + "..."
			}
			fmt.Printf("│ %-*s │ %-*s │ %-*s │\n", maxColNameWidth, col.Name, maxTypeWidth, col.Type, descWidth, desc)
		}
		
		// Print bottom border
		fmt.Print("└")
		fmt.Print(strings.Repeat("─", maxColNameWidth+2))
		fmt.Print("┴")
		fmt.Print(strings.Repeat("─", maxTypeWidth+2))
		fmt.Print("┴")
		fmt.Print(strings.Repeat("─", descWidth+2))
		fmt.Println("┘")
	} else {
		// Show 2-column layout without descriptions
		// Print top border
		fmt.Print("┌")
		fmt.Print(strings.Repeat("─", maxColNameWidth+2))
		fmt.Print("┬")
		fmt.Print(strings.Repeat("─", maxTypeWidth+2))
		fmt.Println("┐")
		
		// Print header
		fmt.Printf("│ %-*s │ %-*s │\n", maxColNameWidth, "Column", maxTypeWidth, "Type")
		
		// Print separator
		fmt.Print("├")
		fmt.Print(strings.Repeat("─", maxColNameWidth+2))
		fmt.Print("┼")
		fmt.Print(strings.Repeat("─", maxTypeWidth+2))
		fmt.Println("┤")
		
		// Print data rows
		for _, col := range columns {
			fmt.Printf("│ %-*s │ %-*s │\n", maxColNameWidth, col.Name, maxTypeWidth, col.Type)
		}
		
		// Print bottom border
		fmt.Print("└")
		fmt.Print(strings.Repeat("─", maxColNameWidth+2))
		fmt.Print("┴")
		fmt.Print(strings.Repeat("─", maxTypeWidth+2))
		fmt.Println("┘")
	}
	
	fmt.Printf("(%d columns)\n", len(columns))

	return nil
}

func (c *Client) findRawTableData(schema map[string]interface{}, tableName string) map[string]interface{} {
	// Try to find the raw table data for debugging
	if tables, exists := schema["tables"]; exists {
		if tableList, ok := tables.([]interface{}); ok {
			for _, table := range tableList {
				if tableMap, ok := table.(map[string]interface{}); ok {
					if name, exists := tableMap["name"]; exists {
						if nameStr, ok := name.(string); ok && strings.EqualFold(nameStr, tableName) {
							return tableMap
						}
					}
				}
			}
		} else if tableMap, ok := tables.(map[string]interface{}); ok {
			for name, tableData := range tableMap {
				if strings.EqualFold(name, tableName) {
					if tableInfo, ok := tableData.(map[string]interface{}); ok {
						return tableInfo
					}
				}
			}
		}
	}
	
	// Try nested schemas
	if schemas, exists := schema["schemas"]; exists {
		if schemaMap, ok := schemas.(map[string]interface{}); ok {
			for _, schemaValue := range schemaMap {
				if schemaData, ok := schemaValue.(map[string]interface{}); ok {
					if tables, exists := schemaData["tables"]; exists {
						if tableMap, ok := tables.(map[string]interface{}); ok {
							for name, tableData := range tableMap {
								if strings.EqualFold(name, tableName) {
									if tableInfo, ok := tableData.(map[string]interface{}); ok {
										return tableInfo
									}
								}
							}
						}
					}
				}
			}
		}
	}
	
	// Try direct table keys
	for key, value := range schema {
		if strings.EqualFold(key, tableName) {
			if tableData, ok := value.(map[string]interface{}); ok {
				return tableData
			}
		}
	}
	
	return nil
}

type TableColumn struct {
	Name        string
	Type        string
	Description string
}

func (c *Client) extractAllTableSchemas(schema map[string]interface{}) map[string][]TableColumn {
	tableSchemas := make(map[string][]TableColumn)
	
	// Pattern 1: Direct tables array
	if tables, exists := schema["tables"]; exists {
		if tableList, ok := tables.([]interface{}); ok {
			for _, table := range tableList {
				if tableMap, ok := table.(map[string]interface{}); ok {
					if name, exists := tableMap["name"]; exists {
						if nameStr, ok := name.(string); ok {
							columns := c.extractColumns(tableMap)
							tableSchemas[nameStr] = columns
						}
					}
				}
			}
		} else if tableMap, ok := tables.(map[string]interface{}); ok {
			// Pattern 1b: Tables as a map
			for tableName, tableData := range tableMap {
				if tableInfo, ok := tableData.(map[string]interface{}); ok {
					columns := c.extractColumns(tableInfo)
					tableSchemas[tableName] = columns
				}
			}
		}
	}
	
	// Pattern 2: Nested schemas structure
	if schemas, exists := schema["schemas"]; exists {
		if schemaMap, ok := schemas.(map[string]interface{}); ok {
			for _, schemaValue := range schemaMap {
				if schemaData, ok := schemaValue.(map[string]interface{}); ok {
					if tables, exists := schemaData["tables"]; exists {
						if tableMap, ok := tables.(map[string]interface{}); ok {
							for tableName, tableData := range tableMap {
								if tableInfo, ok := tableData.(map[string]interface{}); ok {
									columns := c.extractColumns(tableInfo)
									tableSchemas[tableName] = columns
								}
							}
						}
					}
				}
			}
		}
	}
	
	// Pattern 3: Direct table names as top-level keys
	if len(tableSchemas) == 0 {
		for key, value := range schema {
			// Skip common non-table keys
			if key == "version" || key == "metadata" || key == "info" {
				continue
			}
			
			if tableData, ok := value.(map[string]interface{}); ok {
				// Check if this looks like table data
				if _, hasColumns := tableData["columns"]; hasColumns {
					columns := c.extractColumns(tableData)
					if len(columns) > 0 {
						tableSchemas[key] = columns
					}
				}
			}
		}
	}
	
	return tableSchemas
}

func (c *Client) extractColumns(tableData map[string]interface{}) []TableColumn {
	var columns []TableColumn
	
	// Pattern 0: columnInfo (Soracom format)
	if cols, exists := tableData["columnInfo"]; exists {
		if colList, ok := cols.([]interface{}); ok {
			for _, col := range colList {
				if colMap, ok := col.(map[string]interface{}); ok {
					name := ""
					colType := ""
					description := ""
					
					if n, exists := colMap["name"]; exists {
						if nameStr, ok := n.(string); ok {
							name = nameStr
						}
					}
					
					// Prefer databaseType over type for better precision
					if t, exists := colMap["databaseType"]; exists {
						if typeStr, ok := t.(string); ok {
							colType = typeStr
						}
					} else if t, exists := colMap["type"]; exists {
						if typeStr, ok := t.(string); ok {
							colType = typeStr
						}
					}
					
					if d, exists := colMap["description"]; exists {
						if descStr, ok := d.(string); ok {
							description = descStr
						}
					}
					
					if name != "" {
						if colType == "" {
							colType = "UNKNOWN"
						}
						columns = append(columns, TableColumn{Name: name, Type: colType, Description: description})
					}
				}
			}
		}
	}
	
	// Pattern 1: columns as array of objects with name/type
	if len(columns) == 0 && tableData["columns"] != nil {
		if cols, exists := tableData["columns"]; exists {
			if colList, ok := cols.([]interface{}); ok {
				for _, col := range colList {
					if colMap, ok := col.(map[string]interface{}); ok {
						name := ""
						colType := ""
						
						// Try different name fields
						if n, exists := colMap["name"]; exists {
							if nameStr, ok := n.(string); ok {
								name = nameStr
							}
						} else if n, exists := colMap["column_name"]; exists {
							if nameStr, ok := n.(string); ok {
								name = nameStr
							}
						}
						
						// Try different type fields
						if t, exists := colMap["type"]; exists {
							if typeStr, ok := t.(string); ok {
								colType = typeStr
							}
						} else if t, exists := colMap["data_type"]; exists {
							if typeStr, ok := t.(string); ok {
								colType = typeStr
							}
						} else if t, exists := colMap["column_type"]; exists {
							if typeStr, ok := t.(string); ok {
								colType = typeStr
							}
						}
						
						if name != "" {
							if colType == "" {
								colType = "UNKNOWN"
							}
							columns = append(columns, TableColumn{Name: name, Type: colType, Description: ""})
						}
					}
				}
			}
			
			// Pattern 2: columns as map of column_name -> column_info
			if colMap, ok := cols.(map[string]interface{}); ok {
				for colName, colData := range colMap {
					colType := "UNKNOWN"
					
					if colInfo, ok := colData.(map[string]interface{}); ok {
						// Try different type field names
						if t, exists := colInfo["type"]; exists {
							if typeStr, ok := t.(string); ok {
								colType = typeStr
							}
						} else if t, exists := colInfo["data_type"]; exists {
							if typeStr, ok := t.(string); ok {
								colType = typeStr
							}
						}
					} else if typeStr, ok := colData.(string); ok {
						// Pattern 2b: column_name -> type_string directly
						colType = typeStr
					}
					
					columns = append(columns, TableColumn{Name: colName, Type: colType, Description: ""})
				}
			}
		}
	}
	
	// Pattern 3: fields instead of columns
	if len(columns) == 0 {
		if fields, exists := tableData["fields"]; exists {
			if fieldList, ok := fields.([]interface{}); ok {
				for _, field := range fieldList {
					if fieldMap, ok := field.(map[string]interface{}); ok {
						name := ""
						colType := ""
						
						if n, exists := fieldMap["name"]; exists {
							if nameStr, ok := n.(string); ok {
								name = nameStr
							}
						}
						
						if t, exists := fieldMap["type"]; exists {
							if typeStr, ok := t.(string); ok {
								colType = typeStr
							}
						}
						
						if name != "" {
							if colType == "" {
								colType = "UNKNOWN"
							}
							columns = append(columns, TableColumn{Name: name, Type: colType, Description: ""})
						}
					}
				}
			}
		}
	}
	
	return columns
}

func (c *Client) showPlans() error {
	fmt.Println("--------------------------------------------------")
	fmt.Println("show plans")
	
	body, err := c.makeRequest("GET", fmt.Sprintf("https://%s/v1/analysis/plans/query", c.baseURL), nil)
	if err != nil {
		return err
	}

	var prettyJSON bytes.Buffer
	if err := json.Indent(&prettyJSON, body, "", "  "); err != nil {
		fmt.Println(string(body))
	} else {
		fmt.Println(prettyJSON.String())
	}

	return nil
}

func (c *Client) executeQuery(sqlQuery string, openFile bool) error {
	if c.debug {
		fmt.Printf("Executing SQL: %s\n", sqlQuery)
		if c.fromTime > 0 {
			fmt.Printf("From time: %d (%s)\n", c.fromTime, time.Unix(c.fromTime, 0).Format(time.RFC3339))
		}
		if c.toTime > 0 {
			fmt.Printf("To time: %d (%s)\n", c.toTime, time.Unix(c.toTime, 0).Format(time.RFC3339))
		}
	}

	// Create payload with optional time parameters
	payload := map[string]interface{}{"sql": sqlQuery}
	if c.fromTime > 0 {
		payload["from"] = c.fromTime
	}
	if c.toTime > 0 {
		payload["to"] = c.toTime
	}

	body, err := c.makeRequest("POST", fmt.Sprintf("https://%s/v1/analysis/queries", c.baseURL), payload)
	if err != nil {
		return err
	}

	var queryResp QueryResponse
	if err := json.Unmarshal(body, &queryResp); err != nil {
		return fmt.Errorf("failed to parse query response: %v", err)
	}

	if c.debug {
		fmt.Printf("Query ID: %s\n", queryResp.QueryId)
	}

	// Show animation while waiting for query to complete (unless in debug or silent mode)
	var stopAnimation chan bool
	var stopCancel chan bool
	cancelQuery := make(chan bool)
	if !c.debug && !c.silent {
		stopAnimation = make(chan bool)
		stopCancel = make(chan bool)
		go c.showQueryAnimation(stopAnimation, cancelQuery)
		go c.watchForCancel(cancelQuery, stopCancel)
	}
	
	// Wait for either timeout or cancellation
	select {
	case <-time.After(5 * time.Second):
		// Normal timeout - continue with query
		if !c.debug && !c.silent {
			stopAnimation <- true
			stopCancel <- true // Stop the key monitoring
		}
	case <-cancelQuery:
		// User cancelled - stop animation and return error
		if !c.debug && !c.silent {
			stopAnimation <- true
			stopCancel <- true // Stop the key monitoring
		}
		return fmt.Errorf("query cancelled by user")
	}

	// Poll for query completion with retry logic
	var statusResp QueryStatusResponse
	maxRetries := 30 // Max 30 retries (about 5 minutes with 10 second intervals)
	for i := 0; i < maxRetries; i++ {
		body, err = c.makeRequest("GET", fmt.Sprintf("https://%s/v1/analysis/queries/%s?exportFormat=jsonl", c.baseURL, queryResp.QueryId), nil)
		if err != nil {
			return err
		}

		if c.debug {
			fmt.Printf("Status check %d: %s\n", i+1, string(body))
		}

		if err := json.Unmarshal(body, &statusResp); err != nil {
			return fmt.Errorf("failed to parse status response: %v", err)
		}

		// Check if query is completed
		if statusResp.Status == "COMPLETED" {
			if c.debug {
				fmt.Printf("Query completed after %d status checks\n", i+1)
			}
			break
		}
		
		// If status is EXPORTING or RUNNING, wait and retry
		if statusResp.Status == "EXPORTING" || statusResp.Status == "RUNNING" {
			if c.debug {
				fmt.Printf("Query status: %s, waiting 10 seconds before retry...\n", statusResp.Status)
			}
			time.Sleep(10 * time.Second)
			continue
		}
		
		// If status is FAILED or other error state, return error
		if statusResp.Status == "FAILED" {
			return fmt.Errorf("query failed: %s", string(body))
		}
		
		// For any other status, wait and retry
		if c.debug {
			fmt.Printf("Unknown status: %s, waiting 5 seconds before retry...\n", statusResp.Status)
		}
		time.Sleep(5 * time.Second)
	}
	
	// Check if we exhausted retries without completion
	if statusResp.Status != "COMPLETED" {
		return fmt.Errorf("query did not complete within timeout, final status: %s", statusResp.Status)
	}

	filename := filepath.Base(strings.Split(statusResp.URL, "?")[0])
	if c.debug {
		fmt.Printf("File Name: %s\n", filename)
	}

	tmpPath := filepath.Join("/tmp", filename)
	if err := c.downloadFile(statusResp.URL, tmpPath); err != nil {
		return fmt.Errorf("failed to download file: %v", err)
	}

	if c.debug {
		fmt.Printf("Downloaded to: %s\n", tmpPath)
	}

	decompressedPath := strings.TrimSuffix(tmpPath, ".gz")
	if err := c.decompressFile(tmpPath, decompressedPath); err != nil {
		return fmt.Errorf("failed to decompress file: %v", err)
	}

	if c.debug {
		if info, err := os.Stat(decompressedPath); err == nil {
			fmt.Printf("Decompressed file size: %d bytes\n", info.Size())
		}
	}

	if openFile {
		if err := c.openInEditor(decompressedPath); err != nil {
			fmt.Printf("Warning: failed to open file in editor: %v\n", err)
		}
	}

	return c.displayJSONFile(decompressedPath, statusResp.ColumnInfo)
}

func (c *Client) callSQLAssistant(context, existingQuery string) (*SQLAssistantResponse, error) {
	request := SQLAssistantRequest{
		Messages: []SQLAssistantMessage{
			{
				Role:      "user",
				Context:   context,
				AgentMode: false,
			},
		},
		TimeRange: SQLAssistantTimeRange{
			Hours: 2,
		},
		ExistingQuery: existingQuery,
	}

	// Save current custom headers and temporarily add SQL helper flag
	originalHeaders := make(map[string]string)
	for k, v := range c.customHeaders {
		originalHeaders[k] = v
	}
	c.customHeaders["x-soracom-dynamicroutes"] = "add-sql-helper"

	url := fmt.Sprintf("https://%s/v1/analysis/sql_assistant", c.baseURL)
	response, err := c.makeRequest("POST", url, request)
	
	// Restore original custom headers
	c.customHeaders = originalHeaders
	
	if err != nil {
		return nil, err
	}

	var sqlAssistantResponse SQLAssistantResponse
	if err := json.Unmarshal(response, &sqlAssistantResponse); err != nil {
		return nil, fmt.Errorf("failed to parse SQL assistant response: %w", err)
	}

	return &sqlAssistantResponse, nil
}

func (c *Client) downloadFile(url, filepath string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	out, err := os.Create(filepath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

func (c *Client) decompressFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	gzReader, err := gzip.NewReader(srcFile)
	if err != nil {
		return err
	}
	defer gzReader.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, gzReader)
	return err
}

func (c *Client) openInEditor(filepath string) error {
	cmd := exec.Command("open", "-e", filepath)
	return cmd.Run()
}


func (c *Client) watchForCancel(cancel chan bool, stop chan bool) {
	// Set terminal to raw mode to read individual key presses
	exec.Command("stty", "-f", "/dev/tty", "cbreak", "min", "0", "time", "1").Run()
	exec.Command("stty", "-f", "/dev/tty", "-echo").Run()
	defer func() {
		// Restore normal terminal mode
		exec.Command("stty", "-f", "/dev/tty", "echo").Run()
		exec.Command("stty", "-f", "/dev/tty", "-cbreak").Run()
		exec.Command("stty", "-f", "/dev/tty", "min", "1", "time", "0").Run()
	}()

	// Poll for input with timeout
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	
	buffer := make([]byte, 1)
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			// Try to read with non-blocking mode
			if n, err := os.Stdin.Read(buffer); err == nil && n > 0 {
				// Check for ESC key (ASCII 27)
				if buffer[0] == 27 {
					select {
					case cancel <- true:
					case <-stop:
					}
					return
				}
			}
		}
	}
}

func (c *Client) showQueryAnimation(stop chan bool, cancel chan bool) {
	spinners := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	frame := 0
	startTime := time.Now()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			// Clear the entire line completely and reset colors
			fmt.Print("\r\033[2K\033[0m")
			return
		case <-cancel:
			// Clear the spinner line and show cancellation
			fmt.Print("\r\033[2K\033[31mQuery cancelled\033[0m\n")
			return
		case <-ticker.C:
			// Calculate elapsed seconds
			elapsed := int(time.Since(startTime).Seconds())
			// Show spinner with message and elapsed time in cyan color
			fmt.Printf("\r\033[2K\033[36m%s Executing query... %ds (press ESC to cancel)\033[0m", 
				spinners[frame%len(spinners)], elapsed)
			frame++
		}
	}
}

func (c *Client) showSQLAssistantAnimation(stop chan bool) {
	spinners := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	frame := 0
	startTime := time.Now()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			// Clear the entire line completely and reset colors
			fmt.Print("\r\033[2K\033[0m")
			return
		case <-ticker.C:
			// Calculate elapsed seconds
			elapsed := int(time.Since(startTime).Seconds())
			// Show spinner with message and elapsed time in cyan color
			fmt.Printf("\r\033[2K\033[36m%s Asking SQL assistant... %ds\033[0m", 
				spinners[frame%len(spinners)], elapsed)
			frame++
		}
	}
}


func (c *Client) displayJSONFile(filepath string, columnInfo []ColumnInfo) error {
	file, err := os.Open(filepath)
	if err != nil {
		return err
	}
	defer file.Close()

	// Parse all rows to determine columns and data
	var rows []map[string]interface{}
	var columnOrder []string

	// Use column info from API response if available
	if len(columnInfo) > 0 {
		for _, col := range columnInfo {
			columnOrder = append(columnOrder, col.Name)
		}
	}

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		var row map[string]interface{}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			// If JSON parsing fails, show the raw line
			fmt.Println(line)
			continue
		}

		rows = append(rows, row)

		// If no column info from API, fall back to tracking column names in order of first appearance
		if len(columnOrder) == 0 {
			columnSet := make(map[string]bool)
			for key := range row {
				if !columnSet[key] {
					columnOrder = append(columnOrder, key)
					columnSet[key] = true
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	if len(rows) == 0 {
		fmt.Println("No results found.")
		return nil
	}

	// Display in the specified format
	switch c.format {
	case "table":
		c.displayTable(columnOrder, rows)
	case "csv":
		c.displayCSV(columnOrder, rows)
	case "json":
		c.displayJSON(rows)
	default:
		c.displayTable(columnOrder, rows) // fallback to table
	}
	return nil
}

func (c *Client) displayTable(columns []string, rows []map[string]interface{}) {
	if len(rows) == 0 {
		return
	}

	// Calculate column widths and determine if column is numeric
	widths := make(map[string]int)
	isNumeric := make(map[string]bool)
	
	for _, col := range columns {
		widths[col] = len(col) // Start with header width
		isNumeric[col] = c.isColumnNumeric(col, rows)
	}

	// Check all data to find max width for each column
	for _, row := range rows {
		for _, col := range columns {
			if val, exists := row[col]; exists {
				str := c.formatValue(val)
				if len(str) > widths[col] {
					widths[col] = len(str)
				}
			}
		}
	}

	// Print header
	fmt.Print("┌")
	for i, col := range columns {
		fmt.Print(strings.Repeat("─", widths[col]+2))
		if i < len(columns)-1 {
			fmt.Print("┬")
		}
	}
	fmt.Println("┐")

	fmt.Print("│")
	for _, col := range columns {
		if isNumeric[col] {
			fmt.Printf(" %*s │", widths[col], col) // Right-align numeric headers
		} else {
			fmt.Printf(" %-*s │", widths[col], col) // Left-align text headers
		}
	}
	fmt.Println()

	// Print separator
	fmt.Print("├")
	for i, col := range columns {
		fmt.Print(strings.Repeat("─", widths[col]+2))
		if i < len(columns)-1 {
			fmt.Print("┼")
		}
	}
	fmt.Println("┤")

	// Print data rows
	for _, row := range rows {
		fmt.Print("│")
		for _, col := range columns {
			val := ""
			if v, exists := row[col]; exists {
				val = c.formatValue(v)
			}
			
			if isNumeric[col] {
				fmt.Printf(" %*s │", widths[col], val) // Right-align numeric values
			} else {
				fmt.Printf(" %-*s │", widths[col], val) // Left-align text values
			}
		}
		fmt.Println()
	}

	// Print bottom border
	fmt.Print("└")
	for i, col := range columns {
		fmt.Print(strings.Repeat("─", widths[col]+2))
		if i < len(columns)-1 {
			fmt.Print("┴")
		}
	}
	fmt.Println("┘")

	// Print row count
	fmt.Printf("\n(%d rows)\n", len(rows))
}

// displayCSV displays results in CSV format
func (c *Client) displayCSV(columns []string, rows []map[string]interface{}) {
	if len(rows) == 0 {
		return
	}

	// Print header
	for i, col := range columns {
		if i > 0 {
			fmt.Print(",")
		}
		// Escape and quote column names if needed
		fmt.Print(c.escapeCSVField(col))
	}
	fmt.Println()

	// Print data rows
	for _, row := range rows {
		for i, col := range columns {
			if i > 0 {
				fmt.Print(",")
			}
			val := ""
			if v, exists := row[col]; exists {
				val = c.formatValue(v)
			}
			fmt.Print(c.escapeCSVField(val))
		}
		fmt.Println()
	}
}

// displayJSON displays results in JSON format
func (c *Client) displayJSON(rows []map[string]interface{}) {
	if len(rows) == 0 {
		fmt.Println("[]")
		return
	}

	// Pretty print JSON
	jsonBytes, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		// Fallback to line-by-line output if pretty printing fails
		fmt.Println("[")
		for i, row := range rows {
			if i > 0 {
				fmt.Print(",")
			}
			rowBytes, _ := json.Marshal(row)
			fmt.Printf("  %s\n", string(rowBytes))
		}
		fmt.Println("]")
		return
	}
	fmt.Println(string(jsonBytes))
}

// escapeCSVField escapes and quotes a CSV field if necessary
func (c *Client) escapeCSVField(field string) string {
	// Check if field contains comma, quote, or newline
	if strings.Contains(field, ",") || strings.Contains(field, "\"") || strings.Contains(field, "\n") || strings.Contains(field, "\r") {
		// Escape quotes by doubling them and wrap in quotes
		escaped := strings.ReplaceAll(field, "\"", "\"\"")
		return fmt.Sprintf("\"%s\"", escaped)
	}
	return field
}

func (c *Client) isColumnNumeric(column string, rows []map[string]interface{}) bool {
	// Check if majority of non-null values in this column are numeric
	numericCount := 0
	totalCount := 0
	
	for _, row := range rows {
		if val, exists := row[column]; exists && val != nil {
			totalCount++
			switch val.(type) {
			case float64, int64, int:
				numericCount++
			}
		}
	}
	
	// Consider column numeric if more than 80% of values are numeric
	return totalCount > 0 && float64(numericCount)/float64(totalCount) > 0.8
}

func (c *Client) formatValue(val interface{}) string {
	if val == nil {
		return "NULL"
	}
	
	switch v := val.(type) {
	case string:
		return v
	case float64:
		// Check if it's a whole number
		if v == float64(int64(v)) {
			return fmt.Sprintf("%.0f", v)
		}
		return fmt.Sprintf("%.2f", v)
	case int64:
		return fmt.Sprintf("%d", v)
	case int:
		return fmt.Sprintf("%d", v)
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", v)
	}
}