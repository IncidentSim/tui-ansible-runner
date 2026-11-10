package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"incidentctl/internal/config"
	"incidentctl/internal/remote"
)

const incidentSimLogo = `  ___           _     _            _   ____  _
 |_ _|_ __  ___(_) __| | ___ _ __ | |_/ ___|(_)_ __ ___
  | || '_ \/ __| |/ _` + "`" + ` |/ _ \ '_ \| __\___ \| | '_ ` + "`" + ` _ \
  | || | | \__ \ | (_| |  __/ | | | |_ ___) | | | | | | |
 |___|_| |_|___/_|\__,_|\___|_| |_|\__|____/|_|_| |_| |_|`

type tuiScreen int

const (
	screenExercises tuiScreen = iota
	screenDescription
	screenTips
	screenTip
	screenSolution
)

type operation int

const (
	operationStart operation = iota
	operationVerify
	operationOpenSSH
)

type tip struct {
	title string
	body  string
}

type exerciseItem struct {
	exercise    config.Exercise
	dir         string
	description string
	tips        []tip
	err         error
}

type operationResult struct {
	kind   operation
	output string
	err    error
}

type tuiModel struct {
	config     *config.Config
	configErr  error
	exercises  []exerciseItem
	screen     tuiScreen
	returnTo   tuiScreen
	tipsReturn tuiScreen
	selected   int
	tipIndex   int
	scroll     int
	width      int
	height     int
	busy       bool
	status     string
	logs       string
}

var (
	logoStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF7A59")).Bold(true)
	titleStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFD166")).Bold(true)
	mutedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#8B95A7"))
	selectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#10131A")).Background(lipgloss.Color("#7FDBCA")).Bold(true).Padding(0, 1)
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF6B6B")).Bold(true)
	successStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#7FDB8A")).Bold(true)
	panelStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#465166")).Padding(0, 1)
)

func runTUI(configPath string) error {
	exercises, err := discoverExercises(exerciseRoot)
	if err != nil {
		return err
	}
	model := tuiModel{exercises: exercises, screen: screenExercises, returnTo: screenExercises}
	if cfg, loadErr := config.Load(configPath); loadErr != nil {
		model.configErr = loadErr
	} else {
		model.config = &cfg
	}
	_, err = tea.NewProgram(model, tea.WithAltScreen()).Run()
	return err
}

func discoverExercises(root string) ([]exerciseItem, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read exercises: %w", err)
	}
	items := make([]exerciseItem, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		item := exerciseItem{dir: filepath.Join(root, entry.Name())}
		item.exercise, item.dir, item.err = config.LoadExercise(root, entry.Name())
		if data, readErr := os.ReadFile(filepath.Join(item.dir, "README.md")); readErr == nil {
			item.description = markdownToText(string(data))
		} else if item.err == nil {
			item.err = fmt.Errorf("read README.md: %w", readErr)
		}
		item.tips, err = loadTips(item.dir)
		if err != nil && item.err == nil {
			item.err = err
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].exercise.Name < items[j].exercise.Name })
	return items, nil
}

func loadTips(dir string) ([]tip, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "tips", "*.md"))
	if err != nil {
		return nil, fmt.Errorf("list tips: %w", err)
	}
	sort.Strings(paths)
	tips := make([]tip, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		body := markdownToText(string(data))
		title := firstLine(body)
		if title == "" {
			title = filepath.Base(path)
		}
		tips = append(tips, tip{title: title, body: body})
	}
	return tips, nil
}

func markdownToText(value string) string {
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	inFence := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			lines[i] = ""
			continue
		}
		if !inFence {
			line = strings.TrimLeft(line, " \t")
			if strings.HasPrefix(line, "#") {
				line = strings.TrimSpace(strings.TrimLeft(line, "#"))
			}
		}
		lines[i] = line
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func firstLine(value string) string {
	for _, line := range strings.Split(value, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

func withoutFirstLine(value string) string {
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != "" {
			return strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
		}
	}
	return ""
}

func (m tuiModel) Init() tea.Cmd { return nil }

func (m tuiModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case operationResult:
		m.busy = false
		m.logs = msg.output
		if msg.err != nil {
			m.status = "Ошибка: " + msg.err.Error()
			return m, nil
		}
		switch msg.kind {
		case operationStart:
			m.status = "Задача готова: инфраструктура создана, инцидент внесён, проверка ожидаемо падает."
		case operationVerify:
			m.status = "Проверка решения прошла успешно."
		case operationOpenSSH:
			m.status = "SSH-сессия открыта в отдельном окне терминала."
		}
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.busy {
			return m, nil
		}
		return m.updateKey(msg.String())
	}
	return m, nil
}

func (m tuiModel) updateKey(key string) (tea.Model, tea.Cmd) {
	switch m.screen {
	case screenExercises:
		switch key {
		case "q":
			return m, tea.Quit
		case "up", "k":
			m.selected = max(0, m.selected-1)
		case "down", "j":
			m.selected = min(len(m.exercises)-1, m.selected+1)
		case "enter":
			if len(m.exercises) > 0 {
				m.screen, m.returnTo, m.scroll = screenDescription, screenExercises, 0
			}
		}
	case screenDescription:
		switch key {
		case "esc", "backspace", "q":
			m.screen, m.scroll = m.returnTo, 0
		case "up", "k":
			m.scroll = max(0, m.scroll-1)
		case "down", "j":
			m.scroll++
		case "t":
			m.screen, m.tipsReturn, m.tipIndex, m.scroll = screenTips, screenDescription, 0, 0
		case "s":
			return m.startSolution()
		}
	case screenTips:
		switch key {
		case "esc", "backspace", "q":
			m.screen = m.tipsReturn
		case "up", "k":
			m.tipIndex = max(0, m.tipIndex-1)
		case "down", "j":
			m.tipIndex = min(len(m.current().tips)-1, m.tipIndex+1)
		case "enter":
			if len(m.current().tips) > 0 {
				m.screen, m.scroll = screenTip, 0
			}
		}
	case screenTip:
		switch key {
		case "esc", "backspace", "q":
			m.screen, m.scroll = screenTips, 0
		case "up", "k":
			m.scroll = max(0, m.scroll-1)
		case "down", "j":
			m.scroll++
		}
	case screenSolution:
		switch key {
		case "q":
			return m, tea.Quit
		case "d":
			m.screen, m.returnTo, m.scroll = screenDescription, screenSolution, 0
		case "t":
			m.screen, m.tipsReturn, m.tipIndex = screenTips, screenSolution, 0
		case "c":
			if m.config != nil {
				m.status = sshCommand(m.config.Target)
			} else {
				m.status = "SSH недоступен: " + m.configErr.Error()
			}
		case "o":
			return m.openSSH()
		case "v":
			return m.verifySolution()
		case "x", "esc":
			m.screen, m.status, m.logs = screenExercises, "", ""
		}
	}
	return m, nil
}

func (m tuiModel) current() exerciseItem {
	if len(m.exercises) == 0 {
		return exerciseItem{}
	}
	index := min(max(m.selected, 0), len(m.exercises)-1)
	return m.exercises[index]
}

func (m tuiModel) startSolution() (tea.Model, tea.Cmd) {
	item := m.current()
	if item.err != nil {
		m.status = "Упражнение недоступно: " + item.err.Error()
		return m, nil
	}
	if m.config == nil {
		m.status = "Нельзя начать: " + m.configErr.Error()
		return m, nil
	}
	m.screen, m.busy, m.status, m.logs = screenSolution, true, "Подготавливаю инфраструктуру…", ""
	cfg := *m.config
	return m, func() tea.Msg {
		var output bytes.Buffer
		client := remote.NewWithWriters(cfg.Target, &output, &output)
		steps := []struct {
			name     string
			playbook string
		}{
			{"Создание инфраструктуры", item.exercise.Playbooks.Infra},
			{"Внесение инцидента", item.exercise.Playbooks.Incident},
		}
		for _, step := range steps {
			fmt.Fprintf(&output, "\n━━ %s ━━\n", step.name)
			if err := runPlaybook(client, cfg, item.exercise, item.dir, step.playbook, nil); err != nil {
				return operationResult{kind: operationStart, output: output.String(), err: err}
			}
		}
		fmt.Fprintln(&output, "\n━━ Проверка ожидаемого провала ━━")
		err := runPlaybook(client, cfg, item.exercise, item.dir, item.exercise.Playbooks.Verification, nil)
		if err == nil {
			return operationResult{kind: operationStart, output: output.String(), err: errors.New("verification неожиданно прошла после внесения инцидента")}
		}
		if !isExpectedVerificationFailure(err) {
			return operationResult{kind: operationStart, output: output.String(), err: fmt.Errorf("не удалось подтвердить ожидаемый провал verification: %w", err)}
		}
		fmt.Fprintln(&output, "verification завершилась с ненулевым кодом — ожидаемый результат.")
		return operationResult{kind: operationStart, output: output.String()}
	}
}

func isExpectedVerificationFailure(err error) bool {
	var runErr *playbookRunError
	var commandErr *remote.CommandError
	// OpenSSH reserves 255 for connection/authentication failures. Those must
	// never be mistaken for the verification playbook detecting the incident.
	return errors.As(err, &runErr) && runErr.Phase == "execute playbook" &&
		errors.As(err, &commandErr) && commandErr.ExitCode != 255
}

func (m tuiModel) verifySolution() (tea.Model, tea.Cmd) {
	if m.config == nil {
		m.status = "Проверка недоступна: " + m.configErr.Error()
		return m, nil
	}
	item, cfg := m.current(), *m.config
	m.busy, m.status = true, "Проверяю решение…"
	return m, func() tea.Msg {
		var output bytes.Buffer
		client := remote.NewWithWriters(cfg.Target, &output, &output)
		err := runPlaybook(client, cfg, item.exercise, item.dir, item.exercise.Playbooks.Verification, nil)
		return operationResult{kind: operationVerify, output: output.String(), err: err}
	}
}

func (m tuiModel) openSSH() (tea.Model, tea.Cmd) {
	if m.config == nil {
		m.status = "SSH недоступен: " + m.configErr.Error()
		return m, nil
	}
	target := m.config.Target
	m.busy, m.status = true, "Открываю отдельное окно терминала…"
	return m, func() tea.Msg {
		err := openSSHTerminal(target)
		return operationResult{kind: operationOpenSSH, err: err}
	}
}

func sshCommand(target config.TargetConfig) string {
	args := []string{"ssh", "-p", strconv.Itoa(target.Port), "-o", "BatchMode=yes"}
	if target.IdentityFile != "" {
		args = append(args, "-i", target.IdentityFile)
	}
	strict := "accept-new"
	if target.StrictHostKeyChecking {
		strict = "yes"
	}
	args = append(args, "-o", "StrictHostKeyChecking="+strict, target.User+"@"+target.Host)
	for i := range args {
		args[i] = localShellQuote(args[i])
	}
	return strings.Join(args, " ")
}

func localShellQuote(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\r\n'\"\\$`!&;|<>()[]{}*?") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func openSSHTerminal(target config.TargetConfig) error {
	command := sshCommand(target)
	switch runtime.GOOS {
	case "darwin":
		script := strings.ReplaceAll(strings.ReplaceAll(command, `\`, `\\`), `"`, `\"`)
		return exec.Command("osascript",
			"-e", `tell application "Terminal"`,
			"-e", `do script "`+script+`"`,
			"-e", "activate",
			"-e", "end tell",
		).Run()
	case "linux":
		terminalScript := command + `; exec "${SHELL:-/bin/sh}" -l`
		candidates := [][]string{
			{"x-terminal-emulator", "-e", "sh", "-lc", terminalScript},
			{"gnome-terminal", "--", "sh", "-lc", terminalScript},
			{"konsole", "-e", "sh", "-lc", terminalScript},
			{"xterm", "-e", "sh", "-lc", terminalScript},
		}
		for _, candidate := range candidates {
			if _, err := exec.LookPath(candidate[0]); err != nil {
				continue
			}
			cmd := exec.Command(candidate[0], candidate[1:]...)
			if err := cmd.Start(); err != nil {
				return err
			}
			return cmd.Process.Release()
		}
		return errors.New("не найден поддерживаемый эмулятор терминала")
	case "windows":
		return exec.Command("cmd", "/c", "start", "", "cmd", "/k", command).Run()
	default:
		return fmt.Errorf("открытие нового терминала не поддерживается на %s", runtime.GOOS)
	}
}

func (m tuiModel) View() string {
	width := m.width
	if width < 60 {
		width = 80
	}
	header := logoStyle.Render(incidentSimLogo) + "\n" + mutedStyle.Render("Интерактивный тренажёр инцидентов")
	body := ""
	switch m.screen {
	case screenExercises:
		body = m.viewExercises(width)
	case screenDescription:
		body = m.viewDescription(width)
	case screenTips:
		body = m.viewTips(width)
	case screenTip:
		body = m.viewTip(width)
	case screenSolution:
		body = m.viewSolution(width)
	}
	return header + "\n\n" + body
}

func (m tuiModel) viewExercises(width int) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Задачи"))
	b.WriteString("\n\n")
	if len(m.exercises) == 0 {
		b.WriteString(errorStyle.Render("Упражнения не найдены."))
	}
	for i, item := range m.exercises {
		name := item.exercise.Name
		if name == "" {
			name = filepath.Base(item.dir)
		}
		line := fmt.Sprintf("%d. %s", i+1, name)
		if i == m.selected {
			line = selectedStyle.Render("› " + line)
		} else {
			line = "  " + line
		}
		if item.err != nil {
			line += errorStyle.Render("  недоступно")
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n" + mutedStyle.Render("↑/↓ выбрать   enter описание   q выход"))
	return panelStyle.Width(max(30, width-4)).Render(b.String())
}

func (m tuiModel) viewDescription(width int) string {
	item := m.current()
	content := titleStyle.Render("Описание · "+item.exercise.Name) + "\n\n" + m.scrollText(item.description, width-8)
	if item.err != nil {
		content += "\n\n" + errorStyle.Render(item.err.Error())
	}
	if m.status != "" && m.returnTo == screenExercises {
		content += "\n\n" + errorStyle.Render(m.status)
	}
	footer := "\n\n" + mutedStyle.Render("j/k прокрутка   t подсказки   s начать решение   esc назад")
	return panelStyle.Width(max(30, width-4)).Render(content + footer)
}

func (m tuiModel) viewTips(width int) string {
	item := m.current()
	var b strings.Builder
	b.WriteString(titleStyle.Render("Подсказки · " + item.exercise.Name))
	b.WriteString("\n\n")
	if len(item.tips) == 0 {
		b.WriteString(mutedStyle.Render("Подсказок пока нет."))
	}
	for i, hint := range item.tips {
		line := fmt.Sprintf("%d. %s", i+1, hint.title)
		if i == m.tipIndex {
			line = selectedStyle.Render("› " + line)
		} else {
			line = "  " + line
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n" + mutedStyle.Render("↑/↓ выбрать   enter открыть   esc назад"))
	return panelStyle.Width(max(30, width-4)).Render(b.String())
}

func (m tuiModel) viewTip(width int) string {
	item := m.current()
	if len(item.tips) == 0 {
		return m.viewTips(width)
	}
	hint := item.tips[min(m.tipIndex, len(item.tips)-1)]
	content := titleStyle.Render(hint.title) + "\n\n" + m.scrollText(withoutFirstLine(hint.body), width-8)
	content += "\n\n" + mutedStyle.Render("j/k прокрутка   esc к списку подсказок")
	return panelStyle.Width(max(30, width-4)).Render(content)
}

func (m tuiModel) viewSolution(width int) string {
	item := m.current()
	state := m.status
	if m.busy {
		state = "◌ " + state
	} else if strings.HasPrefix(state, "Ошибка") {
		state = errorStyle.Render(state)
	} else if state != "" {
		state = successStyle.Render(state)
	}
	if state == "" {
		state = mutedStyle.Render("Решение активно.")
	}
	logHeight := max(5, min(14, m.height-18))
	logs := tailLines(m.logs, logHeight)
	if logs == "" {
		logs = mutedStyle.Render("Вывод команд появится здесь.")
	}
	content := titleStyle.Render("Решение · "+item.exercise.Name) + "\n\n" + state + "\n\n"
	content += mutedStyle.Render("Последний вывод") + "\n" + logs + "\n\n"
	content += mutedStyle.Render("d описание   t подсказки   c SSH-команда   o открыть SSH   v проверить   x выйти из решения   q выйти")
	return panelStyle.Width(max(30, width-4)).Render(content)
}

func (m tuiModel) scrollText(text string, width int) string {
	wrapped := lipgloss.NewStyle().Width(max(20, width)).Render(text)
	lines := strings.Split(wrapped, "\n")
	visible := max(6, m.height-17)
	maxScroll := max(0, len(lines)-visible)
	start := min(m.scroll, maxScroll)
	end := min(len(lines), start+visible)
	return strings.Join(lines[start:end], "\n")
}

func tailLines(value string, count int) string {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}
	return strings.Join(lines, "\n")
}
