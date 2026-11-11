package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"incidentctl/internal/config"
	"incidentctl/internal/exercises"
	"incidentctl/internal/identity"
	"incidentctl/internal/remote"
	"incidentctl/internal/vm"
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
	screenToken
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
	kind       operation
	output     string
	connection *vm.Connection
	cleanup    func()
	status     string
	err        error
}

const (
	tokenExpiredMessage = "токен просрочен, обновите токен перед решением задач"
	serverBusyMessage   = "сейчас сервер занят, попробуйте подключиться позже"
)

type tuiModel struct {
	provider         vm.ConnectionProvider
	remoteWorkDir    string
	connection       *vm.Connection
	sshCleanup       func()
	exercises        []exerciseItem
	screen           tuiScreen
	returnTo         tuiScreen
	tipsReturn       tuiScreen
	selected         int
	tipIndex         int
	scroll           int
	width            int
	height           int
	busy             bool
	status           string
	logs             string
	tokenFile        string
	token            string
	tokenInput       string
	tokenInstalledAt int64
	tokenReturn      tuiScreen
	tokenError       string
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

func runTUI(source exercises.Source, provider vm.ConnectionProvider, remoteWorkDir, tokenFile string) error {
	loaded, err := source.List(context.Background())
	if err != nil {
		return err
	}
	items := make([]exerciseItem, 0, len(loaded))
	for _, item := range loaded {
		tips := make([]tip, 0, len(item.Tips))
		for _, loadedTip := range item.Tips {
			tips = append(tips, tip{title: loadedTip.Title, body: loadedTip.Body})
		}
		items = append(items, exerciseItem{
			exercise:    item.Exercise,
			dir:         item.Dir,
			description: item.Description,
			tips:        tips,
			err:         item.Err,
		})
	}
	model := tuiModel{
		provider:      provider,
		remoteWorkDir: remoteWorkDir,
		exercises:     items,
		screen:        screenExercises,
		returnTo:      screenExercises,
		tokenFile:     tokenFile,
	}
	if tokenRecord, tokenErr := identity.LoadToken(tokenFile); tokenErr == nil {
		model.token = tokenRecord.Token
		model.tokenInstalledAt = tokenRecord.InstalledAt
	}
	if apiProvider, ok := provider.(vm.APIProvider); ok {
		_, capabilityErr := apiProvider.Capabilities(context.Background())
		switch {
		case capabilityErr == nil:
		case errors.Is(capabilityErr, vm.ErrTokenRequired):
			model.status = "Токен не установлен. Укажите токен перед решением задачи."
		case errors.Is(capabilityErr, vm.ErrTokenExpired):
			model.status = tokenExpiredMessage
		default:
			model.status = "Сервис виртуальных машин недоступен. Попробуйте подключиться позже."
		}
	}
	_, err = tea.NewProgram(model, tea.WithAltScreen()).Run()
	return err
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
		if msg.connection != nil {
			m.releaseSSH()
			m.connection = msg.connection
			m.sshCleanup = msg.cleanup
		}
		if msg.err != nil {
			m.status = "Ошибка: " + msg.err.Error()
			return m, nil
		}
		if msg.status != "" {
			m.status = msg.status
			return m, nil
		}
		switch msg.kind {
		case operationStart:
			if msg.output != "" {
				m.status = "Виртуальная машина готова, веб-терминал открыт."
			} else {
				m.status = "Задача готова: инфраструктура создана, инцидент внесён, проверка ожидаемо падает."
			}
		case operationVerify:
			m.status = "Проверка решения прошла успешно."
		case operationOpenSSH:
			m.status = "SSH-сессия открыта в отдельном окне терминала."
		}
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.releaseSSH()
			return m, tea.Quit
		}
		if m.busy {
			return m, nil
		}
		if m.screen == screenToken {
			return m.updateTokenKey(msg)
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
		case "a":
			return m.startTokenInput(screenExercises)
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
		case "a":
			return m.startTokenInput(screenDescription)
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
			m.releaseSSH()
			return m, tea.Quit
		case "d":
			m.screen, m.returnTo, m.scroll = screenDescription, screenSolution, 0
		case "t":
			m.screen, m.tipsReturn, m.tipIndex = screenTips, screenSolution, 0
		case "c":
			if m.connection != nil {
				m.status = sshCommand(*m.connection)
			} else {
				m.status = "SSH недоступен: подключение к VM ещё не получено"
			}
		case "o":
			return m.openSSH()
		case "v":
			return m.verifySolution()
		case "a":
			return m.startTokenInput(screenSolution)
		case "x", "esc":
			m.releaseSSH()
			m.screen, m.status, m.logs, m.connection = screenExercises, "", "", nil
		}
	}
	return m, nil
}

func (m tuiModel) startTokenInput(returnTo tuiScreen) (tea.Model, tea.Cmd) {
	m.screen = screenToken
	m.tokenReturn = returnTo
	m.tokenInput = ""
	m.tokenError = ""
	return m, nil
}

func (m tuiModel) updateTokenKey(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch message.String() {
	case "esc":
		m.screen = m.tokenReturn
		m.tokenError = ""
	case "backspace", "ctrl+h":
		runes := []rune(m.tokenInput)
		if len(runes) > 0 {
			m.tokenInput = string(runes[:len(runes)-1])
		}
	case "enter":
		return m.saveToken()
	default:
		if message.Type == tea.KeyRunes {
			m.tokenInput += string(message.Runes)
		}
	}
	return m, nil
}

func (m tuiModel) saveToken() (tea.Model, tea.Cmd) {
	record, err := identity.SaveToken(m.tokenFile, m.tokenInput)
	if err != nil {
		m.tokenError = "Не удалось сохранить токен: " + err.Error()
		return m, nil
	}
	if tokenProvider, ok := m.provider.(interface{ SetToken(string) }); ok {
		tokenProvider.SetToken(record.Token)
	}
	m.token = record.Token
	m.tokenInstalledAt = record.InstalledAt
	m.tokenInput = ""
	m.tokenError = ""
	m.status = "Токен сохранён."
	m.screen = m.tokenReturn
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
	if m.provider == nil {
		m.status = "Нельзя начать: провайдер VM не настроен"
		return m, nil
	}
	if m.token == "" {
		return m.startTokenInput(screenDescription)
	}
	apiProvider, ok := m.provider.(vm.APIProvider)
	if !ok {
		m.status = "Провайдер VM не поддерживает API создания виртуальной машины"
		return m, nil
	}
	m.screen, m.busy, m.status, m.logs = screenSolution, true, "Проверяю токен и создаю виртуальную машину…", ""
	remoteWorkDir := m.remoteWorkDir
	return m, func() tea.Msg {
		ctx := context.Background()
		if _, err := apiProvider.Capabilities(ctx); err != nil {
			if errors.Is(err, vm.ErrTokenExpired) {
				return operationResult{kind: operationStart, status: tokenExpiredMessage}
			}
			return operationResult{kind: operationStart, status: serverBusyMessage}
		}
		instance, err := apiProvider.CreateVM(ctx, vm.RequestFromExercise(item.exercise))
		if err != nil {
			if errors.Is(err, vm.ErrTokenExpired) {
				return operationResult{kind: operationStart, status: tokenExpiredMessage}
			}
			return operationResult{kind: operationStart, status: serverBusyMessage}
		}
		terminalURL, err := apiProvider.TerminalURL(ctx, instance.ID)
		if err != nil {
			if errors.Is(err, vm.ErrTokenExpired) {
				return operationResult{kind: operationStart, status: tokenExpiredMessage}
			}
			return operationResult{kind: operationStart, status: serverBusyMessage}
		}
		if err := openBrowserURL(terminalURL); err != nil {
			return operationResult{
				kind:   operationStart,
				output: "Веб-терминал: " + terminalURL,
				err:    fmt.Errorf("не удалось открыть веб-терминал: %w", err),
			}
		}

		connection, cleanup, err := apiProvider.SSHConnection(ctx, instance.ID)
		if err != nil {
			if errors.Is(err, vm.ErrTokenExpired) {
				return operationResult{kind: operationStart, status: tokenExpiredMessage}
			}
			return operationResult{kind: operationStart, status: serverBusyMessage}
		}
		var output bytes.Buffer
		client := remote.NewWithWriters(connection, &output, &output)
		fmt.Fprintln(&output, "\n━━ Подготовка Ansible ━━")
		if err := preparePlaybookHost(client, item.exercise); err != nil {
			return operationResult{
				kind:       operationStart,
				output:     "Веб-терминал: " + terminalURL + "\n" + output.String(),
				connection: &connection,
				cleanup:    cleanup,
				err:        &playbookRunError{Phase: "prepare playbook host", Err: err},
			}
		}
		steps := []struct {
			name     string
			playbook string
		}{
			{"Создание инфраструктуры", item.exercise.Playbooks.Infra},
			{"Внесение инцидента", item.exercise.Playbooks.Incident},
		}
		for _, step := range steps {
			fmt.Fprintf(&output, "\n━━ %s ━━\n", step.name)
			if err := runPlaybook(client, remoteWorkDir, item.exercise, item.dir, step.playbook, nil); err != nil {
				return operationResult{kind: operationStart, output: "Веб-терминал: " + terminalURL + "\n" + output.String(), connection: &connection, cleanup: cleanup, err: err}
			}
		}
		return operationResult{kind: operationStart, output: "Веб-терминал: " + terminalURL + "\n" + output.String(), connection: &connection, cleanup: cleanup}
	}
}

func (m *tuiModel) releaseSSH() {
	if m.sshCleanup != nil {
		m.sshCleanup()
		m.sshCleanup = nil
	}
	m.connection = nil
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
	if m.connection == nil {
		m.status = "Проверка недоступна: подключение к VM ещё не получено"
		return m, nil
	}
	item, connection, remoteWorkDir := m.current(), *m.connection, m.remoteWorkDir
	m.busy, m.status = true, "Проверяю решение…"
	return m, func() tea.Msg {
		var output bytes.Buffer
		client := remote.NewWithWriters(connection, &output, &output)
		err := runPlaybook(client, remoteWorkDir, item.exercise, item.dir, item.exercise.Playbooks.Verification, nil)
		return operationResult{kind: operationVerify, output: output.String(), err: err}
	}
}

func (m tuiModel) openSSH() (tea.Model, tea.Cmd) {
	if m.connection == nil {
		m.status = "SSH недоступен: подключение к VM ещё не получено"
		return m, nil
	}
	target := *m.connection
	m.busy, m.status = true, "Открываю отдельное окно терминала…"
	return m, func() tea.Msg {
		err := openSSHTerminal(target)
		return operationResult{kind: operationOpenSSH, err: err}
	}
}

func sshCommand(target vm.Connection) string {
	args := []string{"ssh", "-p", strconv.Itoa(target.Port), "-o", "BatchMode=yes"}
	if target.IdentityFile != "" {
		args = append(args, "-i", target.IdentityFile)
	}
	if target.Bastion != nil {
		args = append(args, "-J", target.Bastion.User+"@"+target.Bastion.Host+":"+strconv.Itoa(target.Bastion.Port))
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

func openSSHTerminal(target vm.Connection) error {
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

func openBrowserURL(target string) error {
	if strings.TrimSpace(target) == "" {
		return errors.New("пустой URL веб-терминала")
	}
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", target).Run()
	case "linux":
		return exec.Command("xdg-open", target).Run()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", target).Run()
	default:
		return fmt.Errorf("открытие браузера не поддерживается на %s", runtime.GOOS)
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
	case screenToken:
		body = m.viewToken(width)
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
	b.WriteString("\n" + mutedStyle.Render("↑/↓ выбрать   enter описание   a токен   q выход"))
	if m.status != "" {
		b.WriteString("\n\n" + mutedStyle.Render(m.status))
	}
	return panelStyle.Width(max(30, width-4)).Render(b.String())
}

func (m tuiModel) viewDescription(width int) string {
	item := m.current()
	content := titleStyle.Render("Описание · "+item.exercise.Name) + "\n\n" + m.scrollText(item.description, width-8)
	if item.err != nil {
		content += "\n\n" + errorStyle.Render(item.err.Error())
	}
	if m.status != "" {
		content += "\n\n" + mutedStyle.Render(m.status)
	}
	footer := "\n\n" + mutedStyle.Render("j/k прокрутка   t подсказки   a токен   s начать решение   esc назад")
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
	content += mutedStyle.Render("d описание   t подсказки   a токен   x выйти из решения   q выйти")
	return panelStyle.Width(max(30, width-4)).Render(content)
}

func (m tuiModel) viewToken(width int) string {
	content := titleStyle.Render("Токен VM service") + "\n\n"
	if m.token != "" {
		content += mutedStyle.Render(fmt.Sprintf("Токен установлен: %d (Unix time)", m.tokenInstalledAt)) + "\n\n"
	} else {
		content += mutedStyle.Render("Токен ещё не установлен.") + "\n\n"
	}
	masked := strings.Repeat("•", len([]rune(m.tokenInput)))
	if masked == "" {
		masked = mutedStyle.Render("введите токен")
	}
	content += "Токен: " + masked
	if m.tokenError != "" {
		content += "\n\n" + errorStyle.Render(m.tokenError)
	}
	content += "\n\n" + mutedStyle.Render("введите токен   enter сохранить   esc назад")
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
