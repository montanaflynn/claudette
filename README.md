# claudette

A TUI and CLI for viewing Claude Code usage statistics, built with [Charm](https://charm.sh).

## Features

- **Project-based usage tracking**: Automatically scans your Claude Code projects.
- **Detailed Token Breakdown**: View Input, Output, Cache Write, and Cache Read tokens.
- **Flexible Grouping**: Aggregate usage by Hour, Day, Week, Month, or Year.
- **Interactive TUI**: Browse projects and view detailed usage tables interactively.
- **JSON Output**: Export data for use in other tools or scripts.

## Installation

### Homebrew (macOS/Linux)

```bash
brew tap montanaflynn/claudette https://github.com/montanaflynn/claudette
brew install claudette
```

### Scoop (Windows)

```powershell
scoop bucket add claudette https://github.com/montanaflynn/claudette
scoop install claudette
```

### Go Install

```bash
go install github.com/montanaflynn/claudette@latest
```

## Usage

### Interactive Mode (TUI)

Simply run the command without arguments to start the interactive terminal interface:

```bash
claudette
```

- Use **Up/Down** arrows to navigate the project list.
- Press **Enter** to view detailed usage for a project.
- Press **Esc** or **Left** to go back to the project list.
- Press **q** or **Ctrl+C** to quit.

### CLI Mode (JSON Output)

You can output usage data as JSON using the `--json` (or `-j`) flag.

**List all projects and their daily usage:**
```bash
claudette --json
```

**Filter by a specific project:**
```bash
claudette --json --project "my-cool-project"
```

**Group usage by a different period (hour, day, week, month, year):**
```bash
claudette --json --group month
```

### Flags

| Flag | Short | Description |
|------|-------|-------------|
| `--json` | `-j` | Output data as JSON instead of TUI |
| `--project` | `-p` | Filter to a specific project |
| `--group` | `-g` | Group by time period (hour, day, week, month, year). Default: "day" |
| `--version` | `-v` | Show version |
| `--help` | `-h` | Show help |

## Data Sources

Claudette automatically scans for usage logs in:
- `~/.claude/projects/`
- `~/.config/claude/projects/`

Each subdirectory is treated as a project, and all `.jsonl` files are parsed recursively to calculate token usage.

## Tech Stack

- [Bubble Tea](https://github.com/charmbracelet/bubbletea) - TUI framework
- [Lip Gloss](https://github.com/charmbracelet/lipgloss) - Styling
- [Bubbles](https://github.com/charmbracelet/bubbles) - Components
- [Kong](https://github.com/alecthomas/kong) - CLI argument parsing

## License

MIT