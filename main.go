package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/alecthomas/kong"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/charmbracelet/x/term"
	"github.com/montanaflynn/claudette/internal/stats"
)

// version is set at build time via ldflags
var version = "devel"

// CLI defines the command-line interface
var CLI struct {
	JSON    bool   `short:"j" help:"Output data as JSON instead of TUI"`
	Project string `short:"p" help:"Filter to specific project"`
	Group   string `short:"g" enum:"hour,day,week,month,year" default:"day" help:"Group by time period (hour, day, week, month, year)"`
	Version kong.VersionFlag `short:"v" help:"Show version"`

	Projects struct {
		List struct{} `cmd:"" help:"List available projects"`
	} `cmd:"" help:"Manage projects"`

	Status struct{} `cmd:"" help:"Show current session status"`

	TUI struct{} `cmd:"" default:"1" help:"Start the interactive TUI (default)"`
}

func main() {
	ctx := kong.Parse(&CLI,
		kong.Name("claudette"),
		kong.Description("Claude Code usage statistics viewer"),
		kong.UsageOnError(),
		kong.Vars{"version": version},
	)

	switch ctx.Command() {
	case "projects list":
		if err := listProjects(); err != nil {
			ctx.FatalIfErrorf(err)
		}
	case "status":
		if err := showStatus(); err != nil {
			ctx.FatalIfErrorf(err)
		}
	case "tui", "":
		if CLI.JSON {
			if err := outputJSON(CLI.Project, CLI.Group); err != nil {
				ctx.FatalIfErrorf(err)
			}
		} else {
			p := tea.NewProgram(initialModel(), tea.WithAltScreen())
			if _, err := p.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
	default:
		// Handle unexpected commands if any
		fmt.Printf("Unknown command: %s\n", ctx.Command())
		os.Exit(1)
	}
}

func showStatus() error {
	blocks, err := stats.LoadAllSessionBlocks(stats.DefaultSessionDuration)
	if err != nil {
		return err
	}

	active := stats.GetActiveBlock(blocks)
	if active == nil {
		fmt.Println("No active session found")
		return nil
	}

	burn := stats.CalculateBurnRate(active)
	remaining := time.Until(active.EndTime)

	fmt.Printf("Session ID: %s\n", active.ID)
	fmt.Printf("Status:     %s\n", "Active")
	fmt.Printf("Start Time: %s\n", active.StartTime.Local().Format("3:04 PM MST"))
	fmt.Printf("End Time:   %s\n", active.EndTime.Local().Format("3:04 PM MST"))
	fmt.Printf("Duration:   %s / %s\n", time.Since(active.StartTime).Round(time.Second), stats.DefaultSessionDuration)
	fmt.Printf("Remaining:  %s\n", remaining.Round(time.Second))
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf("Input:      %s\n", stats.FormatTokens(active.InputTokens))
	fmt.Printf("Output:     %s\n", stats.FormatTokens(active.OutputTokens))
	fmt.Printf("Cache W:    %s\n", stats.FormatTokens(active.CacheCreation))
	fmt.Printf("Cache R:    %s\n", stats.FormatTokens(active.CacheRead))
	fmt.Printf("Total:      %s\n", stats.FormatTokens(active.TotalTokens()))
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	
	if burn != nil {
		fmt.Printf("Burn Rate:  %.1f tokens/min\n", burn.TokensPerMinute)
	}

	return nil
}

func listProjects() error {
	projects, err := stats.ListProjects()
	if err != nil {
		return err
	}

	for _, p := range projects {
		fmt.Printf("%s\n", p.Name)
	}
	return nil
}

// JSON output types
type JSONOutput struct {
	Projects []ProjectOutput `json:"projects"`
}

type ProjectOutput struct {
	Name  string        `json:"name"`
	Path  string        `json:"path"`
	Usage []UsageOutput `json:"usage"`
}

type UsageOutput struct {
	Period string        `json:"period"`
	Models []ModelOutput `json:"models"`
	Totals TokenCounts   `json:"totals"`
}

type ModelOutput struct {
	Model  string      `json:"model"`
	Tokens TokenCounts `json:"tokens"`
}

type TokenCounts struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheWrite int `json:"cache_write"`
	CacheRead  int `json:"cache_read"`
	Total      int `json:"total"`
}

func outputJSON(projectFilter, groupBy string) error {
	projects, err := stats.ListProjects()
	if err != nil {
		return err
	}

	// Filter to specific project if requested
	if projectFilter != "" {
		var found *stats.Project
		for _, p := range projects {
			if p.Name == projectFilter {
				found = &p
				break
			}
		}
		if found == nil {
			return fmt.Errorf("project not found: %s", projectFilter)
		}
		projects = []stats.Project{*found}
	}

	output := JSONOutput{
		Projects: make([]ProjectOutput, len(projects)),
	}

	for i, p := range projects {
		usage, err := stats.LoadGroupedUsageForProject(p.Path, groupBy)
		if err != nil {
			return err
		}

		proj := ProjectOutput{
			Name:  p.Name,
			Path:  p.Path,
			Usage: make([]UsageOutput, len(usage)),
		}

		for j, u := range usage {
			out := UsageOutput{
				Period: u.Period,
				Models: make([]ModelOutput, len(u.Models)),
				Totals: TokenCounts{
					Input:      u.InputTotal,
					Output:     u.OutputTotal,
					CacheWrite: u.CacheCreateTotal,
					CacheRead:  u.CacheReadTotal,
					Total:      u.InputTotal + u.OutputTotal + u.CacheCreateTotal + u.CacheReadTotal,
				},
			}

			for k, modelName := range u.Models {
				m := u.ByModel[modelName]
				out.Models[k] = ModelOutput{
					Model: modelName,
					Tokens: TokenCounts{
						Input:      m.Input,
						Output:     m.Output,
						CacheWrite: m.CacheCreate,
						CacheRead:  m.CacheRead,
						Total:      m.Input + m.Output + m.CacheCreate + m.CacheRead,
					},
				}
			}

			proj.Usage[j] = out
		}

		output.Projects[i] = proj
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(output)
}

// TUI code below

var (
	appStyle = lipgloss.NewStyle().Padding(1, 2)

	titleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFFDF5")).
			Background(lipgloss.Color("#7C3AED")).
			Padding(0, 1)

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#626262"))
)

type view int

const (
	usageListView view = iota
	usageTableView
	sessionListView
	sessionUsageTableView
)

type model struct {
	list        list.Model
	listReady   bool
	currentView view
	selected    string
	usage       []stats.GroupedUsage
	sessions    []stats.SessionBlock
	width       int
	height      int
	err         error
}

type projectItem struct {
	name string
	path string
}

func (i projectItem) Title() string       { return i.name }
func (i projectItem) Description() string { return i.path }
func (i projectItem) FilterValue() string { return i.name }

type projectsLoadedMsg struct {
	projects []stats.Project
}

type usageLoadedMsg struct {
	usage []stats.GroupedUsage
	err   error
}

type errMsg struct{ err error }

func initialModel() model {
	return model{
		currentView: usageListView,
	}
}

func (m model) Init() tea.Cmd {
	return loadUsageList
}

func loadUsageList() tea.Msg {
	projects, err := stats.ListProjects()
	if err != nil {
		return errMsg{err}
	}
	return projectsLoadedMsg{projects}
}

func loadUsage(projectPath string) tea.Cmd {
	return func() tea.Msg {
		var usage []stats.GroupedUsage
		var err error

		if projectPath == "" {
			usage, err = stats.LoadGroupedUsage("day")
		} else {
			usage, err = stats.LoadGroupedUsageForProject(projectPath, "day")
		}

		return usageLoadedMsg{usage, err}
	}
}

func loadSessionUsage(block stats.SessionBlock) tea.Cmd {
	return func() tea.Msg {
		usage := stats.LoadGroupedUsageForEvents(block.Entries, "hour")
		return usageLoadedMsg{usage, nil}
	}
}

func loadSessions() tea.Msg {
	sessions, err := stats.LoadAllSessionBlocks(stats.DefaultSessionDuration)
	if err != nil {
		return errMsg{err}
	}
	// Sort sessions newest first
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].StartTime.After(sessions[j].StartTime)
	})
	return sessionsLoadedMsg{sessions}
}

type sessionItem struct {
	block stats.SessionBlock
}

func (i sessionItem) Title() string {
	if i.block.IsGap {
		return fmt.Sprintf("Gap: %s", stats.FormatDuration(i.block.EndTime.Sub(i.block.StartTime)))
	}
	activeStr := ""
	if i.block.IsActive {
		activeStr = " (Active)"
	}
	start := i.block.StartTime.Local().Format("Jan 02, 3:04 PM")
	end := i.block.EndTime.Local().Format("3:04 PM MST")
	return fmt.Sprintf("Session: %s - %s%s", start, end, activeStr)
}

func (i sessionItem) Description() string {
	if i.block.IsGap {
		return fmt.Sprintf("%s to %s", i.block.StartTime.Local().Format("3:04 PM"), i.block.EndTime.Local().Format("3:04 PM MST"))
	}
	return fmt.Sprintf("Tokens: %s | Models: %s",
		stats.FormatTokens(i.block.TotalTokens()),
		fmt.Sprintf("%v", i.block.Models),
	)
}

func (i sessionItem) FilterValue() string { return i.Title() }

type sessionsLoadedMsg struct {
	sessions []stats.SessionBlock
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("q", "ctrl+c"))):
			return m, tea.Quit
		case key.Matches(msg, key.NewBinding(key.WithKeys("s"))):
			if m.currentView != sessionListView {
				m.currentView = sessionListView
				return m, loadSessions
			}
		case key.Matches(msg, key.NewBinding(key.WithKeys("u"))):
			if m.currentView != usageListView {
				m.currentView = usageListView
				m.selected = ""
				m.usage = nil
				m.sessions = nil
				return m, loadUsageList
			}
		case key.Matches(msg, key.NewBinding(key.WithKeys("left", "esc"))):
			if m.currentView == usageTableView || m.currentView == sessionUsageTableView {
				prevView := usageListView
				if m.currentView == sessionUsageTableView {
					prevView = sessionListView
				}
				m.currentView = prevView
				m.selected = ""
				m.usage = nil
				return m, nil
			}
			return m, tea.Quit
		case key.Matches(msg, key.NewBinding(key.WithKeys("right", "enter"))):
			if m.currentView == usageListView {
				if item, ok := m.list.SelectedItem().(projectItem); ok {
					m.selected = item.name
					m.currentView = usageTableView
					path := item.path
					if item.name == "All Projects" {
						path = ""
					}
					return m, loadUsage(path)
				}
			} else if m.currentView == sessionListView {
				if item, ok := m.list.SelectedItem().(sessionItem); ok {
					if !item.block.IsGap {
						m.selected = item.Title()
						m.currentView = sessionUsageTableView
						return m, loadSessionUsage(item.block)
					}
				}
			}
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.listReady {
			h, v := appStyle.GetFrameSize()
			m.list.SetSize(msg.Width-h, msg.Height-v-2)
		}

	case projectsLoadedMsg:
		items := []list.Item{
			projectItem{name: "All Projects", path: "Aggregate usage across all projects"},
		}
		for _, p := range msg.projects {
			items = append(items, projectItem{name: p.Name, path: p.Path})
		}
		m.updateList(items, "Claude Code Usage")

	case sessionsLoadedMsg:
		var items []list.Item
		for _, s := range msg.sessions {
			items = append(items, sessionItem{block: s})
		}
		m.updateList(items, "Session History")

	case usageLoadedMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.usage = msg.usage
		}

	case errMsg:
		m.err = msg.err
	}

	if (m.currentView == usageListView || m.currentView == sessionListView) && m.listReady {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m *model) updateList(items []list.Item, title string) {
	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = true

	w, h := m.width-4, m.height-6
	if w < 20 {
		w = 80
	}
	if h < 10 {
		h = 20
	}

	m.list = list.New(items, delegate, w, h)
	m.list.Title = title
	m.list.SetShowHelp(false)
	m.list.SetShowStatusBar(false)
	m.list.SetShowPagination(false)
	m.list.SetFilteringEnabled(true)
	m.list.Styles.Title = titleStyle
	m.listReady = true
}

func (m model) View() string {
	if m.err != nil {
		return appStyle.Render(fmt.Sprintf("Error: %v\n\nPress q to quit", m.err))
	}

	help := helpStyle.Render("[u] usage • [s] sessions • [q] quit")

	switch m.currentView {
	case usageTableView, sessionUsageTableView:
		return m.renderTable()
	case sessionListView, usageListView:
		if !m.listReady {
			loading := "usage"
			if m.currentView == sessionListView {
				loading = "sessions"
			}
			return appStyle.Render(fmt.Sprintf("Loading %s...", loading))
		}

		statusBar := fmt.Sprintf("%d items • page %d/%d", 
			len(m.list.Items()), 
			m.list.Paginator.Page+1, 
			m.list.Paginator.TotalPages)
		
		viewHelp := help
		if m.currentView == usageListView {
			viewHelp = helpStyle.Render("[→] select • [u] usage • [s] sessions • [/] filter • [q] quit")
		}

		return appStyle.Render(m.list.View() + "\n" + helpStyle.Render(statusBar) + "\n" + viewHelp)
	default:
		return ""
	}
}

func (m model) renderTable() string {
	if len(m.usage) == 0 {
		return appStyle.Render(
			titleStyle.Render(m.selected) + "\n\n" +
				"No usage data found\n\n" +
				helpStyle.Render("[←] back • [q] quit"),
		)
	}

	width, _, _ := term.GetSize(os.Stdout.Fd())
	if width == 0 {
		width = m.width
	}
	if width == 0 {
		width = 120
	}
	useShort := width < 100

	formatNum := func(n int) string {
		if useShort {
			return stats.FormatTokensShort(n)
		}
		return stats.FormatTokens(n)
	}

	var rows [][]string
	var totalInput, totalOutput, totalCacheCreate, totalCacheRead int

	for _, u := range m.usage {
		totalInput += u.InputTotal
		totalOutput += u.OutputTotal
		totalCacheCreate += u.CacheCreateTotal
		totalCacheRead += u.CacheReadTotal

		for i, modelName := range u.Models {
			mu := u.ByModel[modelName]
			total := mu.Input + mu.Output + mu.CacheCreate + mu.CacheRead

			periodCell := ""
			if i == 0 {
				periodCell = u.Period
			}

			rows = append(rows, []string{
				periodCell,
				modelName,
				formatNum(mu.Input),
				formatNum(mu.Output),
				formatNum(mu.CacheCreate),
				formatNum(mu.CacheRead),
				formatNum(total),
			})
		}
	}

	totalAll := totalInput + totalOutput + totalCacheCreate + totalCacheRead
	rows = append(rows, []string{
		"Total",
		"",
		formatNum(totalInput),
		formatNum(totalOutput),
		formatNum(totalCacheCreate),
		formatNum(totalCacheRead),
		formatNum(totalAll),
	})

	tbl := table.New().
		Border(lipgloss.NormalBorder()).
		BorderRow(true).
		Headers("Period", "Model", "Input", "Output", "Cache Write", "Cache Read", "Total").
		Rows(rows...).
		StyleFunc(func(row, col int) lipgloss.Style {
			return lipgloss.NewStyle().Padding(0, 1)
		})

	title := titleStyle.Render(m.selected)
	help := helpStyle.Render("[←] back • [q] quit")

	return appStyle.Render(
		title + "\n\n" +
			tbl.String() + "\n\n" +
			help,
	)
}
