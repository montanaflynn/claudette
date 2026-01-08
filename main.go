package main

import (
	"encoding/json"
	"fmt"
	"os"

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
}

func main() {
	ctx := kong.Parse(&CLI,
		kong.Name("claudette"),
		kong.Description("Claude Code usage statistics viewer"),
		kong.UsageOnError(),
		kong.Vars{"version": version},
	)

	switch {
	case CLI.JSON:
		if err := outputJSON(CLI.Project, CLI.Group); err != nil {
			ctx.FatalIfErrorf(err)
		}
	default:
		p := tea.NewProgram(initialModel(), tea.WithAltScreen())
		if _, err := p.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}
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
	projectListView view = iota
	usageTableView
)

type model struct {
	list        list.Model
	listReady   bool
	currentView view
	selected    string
	usage       []stats.GroupedUsage
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
		currentView: projectListView,
	}
}

func (m model) Init() tea.Cmd {
	return loadProjects
}

func loadProjects() tea.Msg {
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

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("q", "ctrl+c"))):
			return m, tea.Quit
		case key.Matches(msg, key.NewBinding(key.WithKeys("left", "esc"))):
			if m.currentView == usageTableView {
				m.currentView = projectListView
				m.selected = ""
				m.usage = nil
				return m, nil
			}
			return m, tea.Quit
		case key.Matches(msg, key.NewBinding(key.WithKeys("right", "enter"))):
			if m.currentView == projectListView {
				if item, ok := m.list.SelectedItem().(projectItem); ok {
					m.selected = item.name
					m.currentView = usageTableView
					path := item.path
					if item.name == "All Projects" {
						path = ""
					}
					return m, loadUsage(path)
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
		m.list.Title = "Claude Code Usage"
		m.list.SetShowHelp(false)
		m.list.SetFilteringEnabled(true)
		m.list.Styles.Title = titleStyle
		m.listReady = true

	case usageLoadedMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.usage = msg.usage
		}

	case errMsg:
		m.err = msg.err
	}

	if m.currentView == projectListView && m.listReady {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m model) View() string {
	if m.err != nil {
		return appStyle.Render(fmt.Sprintf("Error: %v\n\nPress q to quit", m.err))
	}

	switch m.currentView {
	case usageTableView:
		return m.renderTable()
	default:
		if !m.listReady {
			return appStyle.Render("Loading projects...")
		}
		help := helpStyle.Render("[→] select • [/] filter • [q] quit")
		return appStyle.Render(m.list.View() + "\n" + help)
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
