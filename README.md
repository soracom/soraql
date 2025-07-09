# SoraQL

A command-line tool for executing SQL queries against Soracom's data warehouse API. SoraQL provides an intuitive interface for querying telemetry data, exploring schemas, and managing data analysis workflows using your existing Soracom CLI profiles.

## Features

- **Profile-based authentication**: Uses your existing Soracom CLI profiles with automatic endpoint detection
- **Interactive SQL shell**: Full-featured readline interface with profile-aware prompts, command history, tab completion, and multi-line queries
- **Schema exploration**: Browse available tables and their structures
- **Flexible authentication**: Support for both email/password and API key authentication
- **Export capabilities**: Download query results in JSONL format with multiple output formats (table, CSV, JSON)
- **Time window queries**: Filter data by time ranges using various formats
- **Debug mode**: Detailed logging for troubleshooting API interactions

## Installation

### Prerequisites

- Go 1.19 or later
- Soracom CLI configured with appropriate profiles

### Build from source

```bash
git clone https://github.com/soracom/soraql.git
cd soraql
go build -o soraql main.go
```

## Configuration

SoraQL uses your existing Soracom CLI profiles. Configure profiles for the environments you'll be accessing:

```bash
# Configure default profile (production)
soracom configure --profile default
# Select: Japan or Global coverage, then Operator credentials
# Email: your-email@soracom.jp (or use API key)
# Password: [your password]

# Configure additional profiles as needed
soracom configure --profile dev
soracom configure --profile staging
```

### Profile Configuration

Profiles are stored in `~/.soracom/PROFILE.json` and contain:

- **Authentication credentials**: Email/password or authKeyId/authKey
- **Coverage type**: `"jp"` for Japan, `"g"` for Global
- **Optional custom endpoint**: Override default endpoints

## Usage

### Basic Query Execution

```bash
# Query using default profile
soraql -sql "SELECT * FROM SIM_SESSION_EVENTS LIMIT 10"

# Query using specific profile
soraql -profile dev -sql "SELECT COUNT(*) FROM CELL_TOWERS"

# Query with output format
soraql -format csv -sql "SELECT * FROM SIM_SNAPSHOTS LIMIT 5"
```

### Schema Exploration

Retrieve schema information for available tables:

```bash
# Show all tables
soraql -schema

# Show specific table schema
soraql -schema
# Then use: .schema SIM_SESSION_EVENTS
```

### Time Window Queries

Filter data by time ranges:

```bash
# Last 24 hours
soraql -from "-24h" -to "now" -sql "SELECT * FROM SIM_SESSION_EVENTS"

# Specific date range
soraql -from "2024-01-01 00:00:00" -to "2024-01-02 00:00:00" -sql "SELECT * FROM CELL_TOWERS"

# Unix timestamps
soraql -from "1640995200" -to "1641081600" -sql "SELECT * FROM SIM_SNAPSHOTS"
```

### Interactive Mode

Launch the interactive SQL shell:

```bash
# Default profile
soraql

# Specific profile
soraql -profile myprofile
```

The interactive prompt shows your profile name:
```
myprofile> SELECT COUNT(*) FROM SIM_SNAPSHOTS;
myprofile> .tables
myprofile> .schema SIM_SESSION_EVENTS
myprofile> exit
```

#### Interactive Features:
- **Profile-aware prompt**: Shows which profile/credentials you're using
- **Command history**: Navigate with up/down arrows, persistent across sessions (`~/.soraql_history`)
- **Tab completion**: SQL keywords, table names, and functions
- **Multi-line queries**: Automatic continuation until semicolon
- **Inline editing**: Full cursor movement and editing capabilities

#### Special Commands:
- `.tables` - Show all available tables
- `.schema [TABLE_NAME]` - Show table schema
- `.window [show|clear|<from> <to>]` - Manage time window for queries
- `.debug [on|off|show]` - Toggle debug mode
- `.format [table|csv|json|show]` - Set output format
- `.ask <question>` - Ask SQL assistant for help
- `exit`, `quit`, `\q`, `.exit`, `.quit` - Exit interactive mode

### Output Formats

```bash
# Table format (default)
soraql -format table -sql "SELECT * FROM SIM_SNAPSHOTS LIMIT 3"

# CSV format
soraql -format csv -sql "SELECT * FROM SIM_SNAPSHOTS LIMIT 3"

# JSON format
soraql -format json -sql "SELECT * FROM SIM_SNAPSHOTS LIMIT 3"
```

### Debug Mode

Enable detailed logging and automatically open result files:

```bash
soraql -debug -sql "SELECT COUNT(*) FROM SIM_SNAPSHOTS"
soraql -debug -open -sql "SELECT * FROM SIM_SESSION_EVENTS LIMIT 5"
```

### Piped Input

Process multiple queries from stdin:

```bash
echo 'SELECT COUNT(*) FROM SIM_SNAPSHOTS' | soraql
printf "query1\nquery2\nexit\n" | soraql -profile myprofile
```

### Help

Display usage information:

```bash
soraql -h
```

## Architecture

### Authentication Flow
1. Reads profile configuration from `~/.soracom/{profile}.json`
2. Determines endpoint based on `coverageType` and optional `endpoint` field
3. Authenticates via `/v1/auth` endpoint using email/password or API key
5. Uses obtained tokens for subsequent API calls

### Query Execution
1. Submit query to `/v1/analysis/queries` (POST)
2. Poll query status at `/v1/analysis/queries/{queryId}` (GET)
3. Download results from `/v1/analysis/queries/{queryId}?exportFormat=jsonl` (GET)
4. Extract and display results from `/tmp/` directory

### Error Handling
- **SQL Compilation Errors**: Invalid column names, syntax errors (ANA0005)
- **Parameter Errors**: Malformed queries (ANA0011)  
- **HTTP Errors**: Network issues, authentication failures
- **File Processing**: Download and decompression error handling

## Available Tables

Common tables available for querying:
- `SIM_SESSION_EVENTS`: SIM session and connectivity events
- `SIM_SNAPSHOTS`: Point-in-time SIM status information
- `CELL_TOWERS`: Cellular tower location and metadata
- `COUNTRIES`: Country information table
- `HARVEST_DATA`: Harvested data table
- `MCC_MNC`: Mobile country/network codes table

Use `.tables` in interactive mode or `-schema` option to see all available tables.

## Examples

### Basic Queries
```bash
# Count total SIM sessions
soraql -sql "SELECT COUNT(*) FROM SIM_SESSION_EVENTS"

# Recent SIM snapshots
soraql -sql "SELECT * FROM SIM_SNAPSHOTS WHERE TIMESTAMP > '2024-01-01' LIMIT 10"

# Cell tower locations in specific country
soraql -sql "SELECT * FROM CELL_TOWERS WHERE COUNTRY = 'JP' LIMIT 5"
```

### Interactive Session
```bash
$ soraql -profile production
production> .tables
┌────────────────────────────────────────────┐
│ SIM_SESSION_EVENTS                         │
│ SIM_SNAPSHOTS                             │
│ CELL_TOWERS                               │
│ HARVEST_DATA                              │
└────────────────────────────────────────────┘
(4 tables)

production> SELECT COUNT(*) FROM SIM_SESSION_EVENTS;
┌───────────┐
│ COUNT(*)  │
├───────────┤
│    15432  │
└───────────┘
(1 rows)

production> exit
```

## Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## License

This project is licensed under the MIT License - see the LICENSE file for details.