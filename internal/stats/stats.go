package stats

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultSessionDuration = 5 * time.Hour
	BurnRateMinEvents      = 2
)

// Project represents a Claude Code project directory
type Project struct {
	Name       string
	Path       string
	ActualPath string
}

// UsageEvent represents a single token usage record
type UsageEvent struct {
	Timestamp     time.Time
	InputTokens   int
	OutputTokens  int
	CacheCreation int
	CacheRead     int
	Model         string
	Project       string
	EventID       string
}

// TotalTokens returns all tokens (input + output + cache)
func (e *UsageEvent) TotalTokens() int {
	return e.InputTokens + e.OutputTokens + e.CacheCreation + e.CacheRead
}

// NonCacheTokens returns input + output only (for burn rate indicator)
func (e *UsageEvent) NonCacheTokens() int {
	return e.InputTokens + e.OutputTokens
}

// SessionBlock represents a 5-hour billing period
type SessionBlock struct {
	ID              string
	StartTime       time.Time
	EndTime         time.Time
	ActualEndTime   time.Time // Last activity in block
	IsActive        bool
	IsGap           bool
	Entries         []UsageEvent
	InputTokens     int
	OutputTokens    int
	CacheCreation   int
	CacheRead       int
	Models          []string
}

// TotalTokens returns sum of all token types
func (b *SessionBlock) TotalTokens() int {
	return b.InputTokens + b.OutputTokens + b.CacheCreation + b.CacheRead
}

// NonCacheTokens returns input + output only
func (b *SessionBlock) NonCacheTokens() int {
	return b.InputTokens + b.OutputTokens
}

// BurnRate holds rate calculations
type BurnRate struct {
	TokensPerMinute          float64
	TokensPerMinuteIndicator float64 // Non-cache only, for thresholds
}

// DailyUsage holds usage aggregated by day
type DailyUsage struct {
	Date             string
	Models           []string
	InputTotal       int
	OutputTotal      int
	CacheCreateTotal int
	CacheReadTotal   int
	ByModel          map[string]*ModelUsage
}

// GroupedUsage holds usage aggregated by a time period
type GroupedUsage struct {
	Period           string
	Models           []string
	InputTotal       int
	OutputTotal      int
	CacheCreateTotal int
	CacheReadTotal   int
	ByModel          map[string]*ModelUsage
}

// ModelUsage holds per-model token counts
type ModelUsage struct {
	Model       string
	Input       int
	Output      int
	CacheCreate int
	CacheRead   int
}

// ListProjects finds all Claude Code projects
func ListProjects() ([]Project, error) {
	var projects []Project
	seen := make(map[string]bool)

	roots := []string{
		filepath.Join(os.Getenv("HOME"), ".claude", "projects"),
		filepath.Join(os.Getenv("HOME"), ".config", "claude", "projects"),
	}

	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}

			path := filepath.Join(root, entry.Name())
			name := projectNameFromPath(entry.Name())

			if seen[name] {
				continue
			}
			seen[name] = true

			actualPath := findActualPath(path)
			if actualPath == "" {
				actualPath = path // Fallback
			}

			projects = append(projects, Project{
				Name:       name,
				Path:       path,
				ActualPath: actualPath,
			})
		}
	}

	sort.Slice(projects, func(i, j int) bool {
		return projects[i].Name < projects[j].Name
	})

	return projects, nil
}

func findActualPath(projectPath string) string {
	entries, err := os.ReadDir(projectPath)
	if err != nil {
		return ""
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}

		path := filepath.Join(projectPath, entry.Name())
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			var record map[string]interface{}
			if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
				continue
			}

			if cwd := getString(record, "cwd"); cwd != "" {
				return cwd
			}
		}
	}
	return ""
}

func projectNameFromPath(dirName string) string {
	parts := strings.Split(dirName, "-")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return dirName
}

// LoadSessionBlocks loads and groups usage into session blocks
func LoadSessionBlocks(project Project, sessionDuration time.Duration) ([]SessionBlock, error) {
	events, err := parseProjectEvents(project.Path)
	if err != nil {
		return nil, err
	}

	return identifySessionBlocks(events, sessionDuration), nil
}

// LoadAllSessionBlocks loads session blocks across ALL projects
func LoadAllSessionBlocks(sessionDuration time.Duration) ([]SessionBlock, error) {
	projects, err := ListProjects()
	if err != nil {
		return nil, err
	}

	var allEvents []UsageEvent
	dedupeCache := make(map[string]bool)

	for _, project := range projects {
		events, err := parseProjectEventsWithDedupe(project.Path, dedupeCache)
		if err != nil {
			continue
		}
		allEvents = append(allEvents, events...)
	}

	sort.Slice(allEvents, func(i, j int) bool {
		return allEvents[i].Timestamp.Before(allEvents[j].Timestamp)
	})

	return identifySessionBlocks(allEvents, sessionDuration), nil
}

func parseProjectEventsWithDedupe(projectPath string, dedupeCache map[string]bool) ([]UsageEvent, error) {
	var allEvents []UsageEvent
	projectName := projectNameFromPath(filepath.Base(projectPath))

	err := filepath.Walk(projectPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}

		events, err := parseJSONLFile(path, dedupeCache, projectName)
		if err != nil {
			return nil
		}

		allEvents = append(allEvents, events...)
		return nil
	})

	if err != nil {
		return nil, err
	}

	return allEvents, nil
}

// GetActiveBlock returns the currently active session block, if any
func GetActiveBlock(blocks []SessionBlock) *SessionBlock {
	for i := len(blocks) - 1; i >= 0; i-- {
		if blocks[i].IsActive && !blocks[i].IsGap {
			return &blocks[i]
		}
	}
	return nil
}

// parseProjectEvents recursively parses all JSONL files in a project
func parseProjectEvents(projectPath string) ([]UsageEvent, error) {
	var allEvents []UsageEvent
	dedupeCache := make(map[string]bool)
	projectName := projectNameFromPath(filepath.Base(projectPath))

	err := filepath.Walk(projectPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}

		events, err := parseJSONLFile(path, dedupeCache, projectName)
		if err != nil {
			return nil
		}

		allEvents = append(allEvents, events...)
		return nil
	})

	if err != nil {
		return nil, err
	}

	sort.Slice(allEvents, func(i, j int) bool {
		return allEvents[i].Timestamp.Before(allEvents[j].Timestamp)
	})

	return allEvents, nil
}

// parseJSONLFile parses a single JSONL file
func parseJSONLFile(path string, dedupeCache map[string]bool, projectName string) ([]UsageEvent, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var events []UsageEvent
	reader := bufio.NewReader(file)
	var partial []byte

	for {
		line, err := reader.ReadBytes('\n')
		if err != nil && err != io.EOF {
			break
		}

		if len(partial) > 0 {
			line = append(partial, line...)
			partial = nil
		}

		if len(line) == 0 {
			if err == io.EOF {
				break
			}
			continue
		}

		var record map[string]interface{}
		if jsonErr := json.Unmarshal(line, &record); jsonErr != nil {
			partial = line
			if err == io.EOF {
				break
			}
			continue
		}

		event := extractUsageEvent(record, projectName)
		if event == nil {
			if err == io.EOF {
				break
			}
			continue
		}

		// Deduplicate
		fp := generateFingerprint(event)
		if dedupeCache[fp] {
			if err == io.EOF {
				break
			}
			continue
		}
		dedupeCache[fp] = true

		events = append(events, *event)

		if err == io.EOF {
			break
		}
	}

	return events, nil
}

func extractUsageEvent(record map[string]interface{}, projectName string) *UsageEvent {
	usage := findUsage(record)
	if usage == nil {
		return nil
	}

	ts := extractTimestamp(record)
	if ts.IsZero() {
		return nil
	}

	// Model can be at top level or in message
	model := getString(record, "model")
	if model == "" {
		if msg, ok := record["message"].(map[string]interface{}); ok {
			model = getString(msg, "model")
		}
	}

	event := &UsageEvent{
		Timestamp:     ts,
		InputTokens:   getInt(usage, "input_tokens"),
		OutputTokens:  getInt(usage, "output_tokens"),
		CacheCreation: getInt(usage, "cache_creation_input_tokens"),
		CacheRead:     getInt(usage, "cache_read_input_tokens"),
		Model:         model,
		Project:       projectName,
		EventID:       findEventID(record),
	}

	if event.TotalTokens() == 0 {
		return nil
	}

	return event
}

func findUsage(record map[string]interface{}) map[string]interface{} {
	if msg, ok := record["message"].(map[string]interface{}); ok {
		if usage, ok := msg["usage"].(map[string]interface{}); ok {
			return usage
		}
	}
	if usage, ok := record["usage"].(map[string]interface{}); ok {
		return usage
	}
	return nil
}

func extractTimestamp(record map[string]interface{}) time.Time {
	fields := []string{"timestamp", "created_at", "time", "ts", "at"}
	for _, field := range fields {
		if val, ok := record[field]; ok {
			if ts := parseTimestamp(val); !ts.IsZero() {
				return ts
			}
		}
	}
	return time.Time{}
}

func parseTimestamp(val interface{}) time.Time {
	switch v := val.(type) {
	case string:
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t
		}
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return t
		}
	case float64:
		if v > 1e12 {
			return time.UnixMilli(int64(v))
		}
		return time.Unix(int64(v), 0)
	case int64:
		if v > 1e12 {
			return time.UnixMilli(v)
		}
		return time.Unix(v, 0)
	}
	return time.Time{}
}

func findEventID(record map[string]interface{}) string {
	for _, field := range []string{"id", "request_id", "message_id"} {
		if id := getString(record, field); id != "" {
			return id
		}
	}
	if msg, ok := record["message"].(map[string]interface{}); ok {
		if id := getString(msg, "id"); id != "" {
			return id
		}
	}
	return ""
}

func generateFingerprint(event *UsageEvent) string {
	data := fmt.Sprintf("%d:%d:%s:%s",
		event.Timestamp.UnixMilli(),
		event.TotalTokens(),
		event.Model,
		event.EventID,
	)
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:8])
}

// identifySessionBlocks groups entries into 5-hour session blocks
func identifySessionBlocks(entries []UsageEvent, sessionDuration time.Duration) []SessionBlock {
	if len(entries) == 0 {
		return nil
	}

	var blocks []SessionBlock
	now := time.Now()

	var currentBlockStart *time.Time
	var currentEntries []UsageEvent

	for _, entry := range entries {
		if currentBlockStart == nil {
			// First entry - start new block at exact timestamp
			start := entry.Timestamp
			currentBlockStart = &start
			currentEntries = []UsageEvent{entry}
			continue
		}

		timeSinceBlockStart := entry.Timestamp.Sub(*currentBlockStart)
		lastEntry := currentEntries[len(currentEntries)-1]
		timeSinceLastEntry := entry.Timestamp.Sub(lastEntry.Timestamp)

		if timeSinceBlockStart > sessionDuration || timeSinceLastEntry > sessionDuration {
			// Close current block
			block := createBlock(*currentBlockStart, currentEntries, now, sessionDuration)
			blocks = append(blocks, block)

			// Add gap block if significant gap
			if timeSinceLastEntry > sessionDuration {
				if gapBlock := createGapBlock(lastEntry.Timestamp, entry.Timestamp, sessionDuration); gapBlock != nil {
					blocks = append(blocks, *gapBlock)
				}
			}

			// Start new block
			start := floorToHour(entry.Timestamp)
			currentBlockStart = &start
			currentEntries = []UsageEvent{entry}
		} else {
			currentEntries = append(currentEntries, entry)
		}
	}

	// Close last block
	if currentBlockStart != nil && len(currentEntries) > 0 {
		block := createBlock(*currentBlockStart, currentEntries, now, sessionDuration)
		blocks = append(blocks, block)
	}

	return blocks
}

func floorToHour(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, t.Location())
}

func createBlock(startTime time.Time, entries []UsageEvent, now time.Time, sessionDuration time.Duration) SessionBlock {
	endTime := startTime.Add(sessionDuration)
	actualEndTime := entries[len(entries)-1].Timestamp
	isActive := now.Sub(actualEndTime) < sessionDuration && now.Before(endTime)

	block := SessionBlock{
		ID:            startTime.Format(time.RFC3339),
		StartTime:     startTime,
		EndTime:       endTime,
		ActualEndTime: actualEndTime,
		IsActive:      isActive,
		Entries:       entries,
	}

	modelSet := make(map[string]bool)
	for _, e := range entries {
		block.InputTokens += e.InputTokens
		block.OutputTokens += e.OutputTokens
		block.CacheCreation += e.CacheCreation
		block.CacheRead += e.CacheRead
		if e.Model != "" {
			modelSet[e.Model] = true
		}
	}

	for m := range modelSet {
		block.Models = append(block.Models, m)
	}

	return block
}

func createGapBlock(lastActivity, nextActivity time.Time, sessionDuration time.Duration) *SessionBlock {
	gapDuration := nextActivity.Sub(lastActivity)
	if gapDuration <= sessionDuration {
		return nil
	}

	gapStart := lastActivity.Add(sessionDuration)
	return &SessionBlock{
		ID:        fmt.Sprintf("gap-%s", gapStart.Format(time.RFC3339)),
		StartTime: gapStart,
		EndTime:   nextActivity,
		IsGap:     true,
	}
}

// LoadDailyUsage loads and aggregates usage by day and model across all projects
func LoadDailyUsage() ([]DailyUsage, error) {
	projects, err := ListProjects()
	if err != nil {
		return nil, err
	}

	var allEvents []UsageEvent
	dedupeCache := make(map[string]bool)

	for _, project := range projects {
		events, err := parseProjectEventsWithDedupe(project.Path, dedupeCache)
		if err != nil {
			continue
		}
		allEvents = append(allEvents, events...)
	}

	sort.Slice(allEvents, func(i, j int) bool {
		return allEvents[i].Timestamp.Before(allEvents[j].Timestamp)
	})

	return aggregateByDay(allEvents), nil
}

// LoadDailyUsageForProject loads daily usage for a specific project path
func LoadDailyUsageForProject(projectPath string) ([]DailyUsage, error) {
	dedupeCache := make(map[string]bool)
	events, err := parseProjectEventsWithDedupe(projectPath, dedupeCache)
	if err != nil {
		return nil, err
	}

	sort.Slice(events, func(i, j int) bool {
		return events[i].Timestamp.Before(events[j].Timestamp)
	})

	return aggregateByDay(events), nil
}

// LoadGroupedUsage loads usage grouped by the specified period (hour, day, week, month, year)
func LoadGroupedUsage(groupBy string) ([]GroupedUsage, error) {
	projects, err := ListProjects()
	if err != nil {
		return nil, err
	}

	var allEvents []UsageEvent
	dedupeCache := make(map[string]bool)

	for _, project := range projects {
		events, err := parseProjectEventsWithDedupe(project.Path, dedupeCache)
		if err != nil {
			continue
		}
		allEvents = append(allEvents, events...)
	}

	sort.Slice(allEvents, func(i, j int) bool {
		return allEvents[i].Timestamp.Before(allEvents[j].Timestamp)
	})

	return aggregateByPeriod(allEvents, groupBy), nil
}

// LoadGroupedUsageForProject loads grouped usage for a specific project
func LoadGroupedUsageForProject(projectPath, groupBy string) ([]GroupedUsage, error) {
	dedupeCache := make(map[string]bool)
	events, err := parseProjectEventsWithDedupe(projectPath, dedupeCache)
	if err != nil {
		return nil, err
	}

	sort.Slice(events, func(i, j int) bool {
		return events[i].Timestamp.Before(events[j].Timestamp)
	})

	return aggregateByPeriod(events, groupBy), nil
}

// LoadGroupedUsageForEvents aggregates usage for a specific set of events
func LoadGroupedUsageForEvents(events []UsageEvent, groupBy string) []GroupedUsage {
	if groupBy == "project" {
		return aggregateByProject(events)
	}
	return aggregateByPeriod(events, groupBy)
}

func aggregateByProject(events []UsageEvent) []GroupedUsage {
	projectMap := make(map[string]*GroupedUsage)
	var projects []string

	for _, e := range events {
		project := e.Project
		if project == "" {
			project = "unknown"
		}

		if _, ok := projectMap[project]; !ok {
			projectMap[project] = &GroupedUsage{
				Period:  project,
				ByModel: make(map[string]*ModelUsage),
			}
			projects = append(projects, project)
		}

		p := projectMap[project]
		p.InputTotal += e.InputTokens
		p.OutputTotal += e.OutputTokens
		p.CacheCreateTotal += e.CacheCreation
		p.CacheReadTotal += e.CacheRead

		model := shortModelName(e.Model)
		if model == "" {
			model = "unknown"
		}

		if _, ok := p.ByModel[model]; !ok {
			p.ByModel[model] = &ModelUsage{Model: model}
		}
		p.ByModel[model].Input += e.InputTokens
		p.ByModel[model].Output += e.OutputTokens
		p.ByModel[model].CacheCreate += e.CacheCreation
		p.ByModel[model].CacheRead += e.CacheRead
	}

	var result []GroupedUsage
	sort.Strings(projects)
	for _, project := range projects {
		p := projectMap[project]
		for m := range p.ByModel {
			p.Models = append(p.Models, m)
		}
		sort.Strings(p.Models)
		result = append(result, *p)
	}

	return result
}

func aggregateByPeriod(events []UsageEvent, groupBy string) []GroupedUsage {
	periodMap := make(map[string]*GroupedUsage)
	var periods []string

	for _, e := range events {
		period := formatPeriod(e.Timestamp.Local(), groupBy)

		if _, ok := periodMap[period]; !ok {
			periodMap[period] = &GroupedUsage{
				Period:  period,
				ByModel: make(map[string]*ModelUsage),
			}
			periods = append(periods, period)
		}

		p := periodMap[period]
		p.InputTotal += e.InputTokens
		p.OutputTotal += e.OutputTokens
		p.CacheCreateTotal += e.CacheCreation
		p.CacheReadTotal += e.CacheRead

		model := shortModelName(e.Model)
		if model == "" {
			model = "unknown"
		}

		if _, ok := p.ByModel[model]; !ok {
			p.ByModel[model] = &ModelUsage{Model: model}
		}
		p.ByModel[model].Input += e.InputTokens
		p.ByModel[model].Output += e.OutputTokens
		p.ByModel[model].CacheCreate += e.CacheCreation
		p.ByModel[model].CacheRead += e.CacheRead
	}

	var result []GroupedUsage
	for _, period := range periods {
		p := periodMap[period]
		for m := range p.ByModel {
			p.Models = append(p.Models, m)
		}
		sort.Strings(p.Models)
		result = append(result, *p)
	}

	return result
}

func formatPeriod(t time.Time, groupBy string) string {
	switch groupBy {
	case "hour":
		return t.Format("2006-01-02 15:00")
	case "week":
		year, week := t.ISOWeek()
		return fmt.Sprintf("%d-W%02d", year, week)
	case "month":
		return t.Format("2006-01")
	case "year":
		return t.Format("2006")
	default: // day
		return t.Format("Jan 02")
	}
}

func aggregateByDay(events []UsageEvent) []DailyUsage {
	dayMap := make(map[string]*DailyUsage)
	var days []string

	for _, e := range events {
		date := e.Timestamp.Local().Format("2006-01-02")

		if _, ok := dayMap[date]; !ok {
			dayMap[date] = &DailyUsage{
				Date:    date,
				ByModel: make(map[string]*ModelUsage),
			}
			days = append(days, date)
		}

		day := dayMap[date]
		day.InputTotal += e.InputTokens
		day.OutputTotal += e.OutputTokens
		day.CacheCreateTotal += e.CacheCreation
		day.CacheReadTotal += e.CacheRead

		model := shortModelName(e.Model)
		if model == "" {
			model = "unknown"
		}

		if _, ok := day.ByModel[model]; !ok {
			day.ByModel[model] = &ModelUsage{Model: model}
		}
		day.ByModel[model].Input += e.InputTokens
		day.ByModel[model].Output += e.OutputTokens
		day.ByModel[model].CacheCreate += e.CacheCreation
		day.ByModel[model].CacheRead += e.CacheRead
	}

	// Build result with sorted models
	var result []DailyUsage
	for _, date := range days {
		day := dayMap[date]

		// Collect and sort model names
		for m := range day.ByModel {
			day.Models = append(day.Models, m)
		}
		sort.Strings(day.Models)

		result = append(result, *day)
	}

	return result
}

func shortModelName(model string) string {
	if strings.Contains(model, "opus") {
		return "opus-4-5"
	}
	if strings.Contains(model, "sonnet") {
		return "sonnet-4-5"
	}
	if strings.Contains(model, "haiku") {
		return "haiku-4-5"
	}
	return model
}

// CalculateBurnRate calculates tokens/minute and cost/hour for a block
func CalculateBurnRate(block *SessionBlock) *BurnRate {
	if len(block.Entries) < BurnRateMinEvents || block.IsGap {
		return nil
	}

	first := block.Entries[0].Timestamp
	last := block.Entries[len(block.Entries)-1].Timestamp
	durationMinutes := last.Sub(first).Minutes()

	if durationMinutes <= 0 {
		return nil
	}

	return &BurnRate{
		TokensPerMinute:          float64(block.TotalTokens()) / durationMinutes,
		TokensPerMinuteIndicator: float64(block.NonCacheTokens()) / durationMinutes,
	}
}

// Helper functions
func getInt(m map[string]interface{}, key string) int {
	if val, ok := m[key]; ok {
		switch v := val.(type) {
		case float64:
			return int(v)
		case int:
			return v
		case int64:
			return int(v)
		case string:
			if i, err := strconv.Atoi(v); err == nil {
				return i
			}
		}
	}
	return 0
}

func getFloat(m map[string]interface{}, key string) float64 {
	if val, ok := m[key]; ok {
		switch v := val.(type) {
		case float64:
			return v
		case int:
			return float64(v)
		case int64:
			return float64(v)
		}
	}
	return 0
}

func getString(m map[string]interface{}, key string) string {
	if val, ok := m[key]; ok {
		if s, ok := val.(string); ok {
			return s
		}
	}
	return ""
}

// FormatTokens formats token counts with commas
func FormatTokens(n int) string {
	if n < 0 {
		return "-" + FormatTokens(-n)
	}

	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}

	var result strings.Builder
	remainder := len(s) % 3
	if remainder > 0 {
		result.WriteString(s[:remainder])
		if len(s) > remainder {
			result.WriteString(",")
		}
	}

	for i := remainder; i < len(s); i += 3 {
		result.WriteString(s[i : i+3])
		if i+3 < len(s) {
			result.WriteString(",")
		}
	}

	return result.String()
}

// FormatTokensShort formats token counts with K/M/B suffixes
func FormatTokensShort(n int) string {
	if n < 0 {
		return "-" + FormatTokensShort(-n)
	}

	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.2fB", float64(n)/1_000_000_000)
	case n >= 1_000_000:
		return fmt.Sprintf("%.2fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	default:
		return strconv.Itoa(n)
	}
}

// FormatTokensAuto uses short format for large numbers, full format for small
func FormatTokensAuto(n int, maxWidth int) string {
	full := FormatTokens(n)
	if len(full) <= maxWidth {
		return full
	}
	return FormatTokensShort(n)
}

// FormatDuration formats duration for display
func FormatDuration(d time.Duration) string {
	if d < 0 {
		return "now"
	}

	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60

	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}
