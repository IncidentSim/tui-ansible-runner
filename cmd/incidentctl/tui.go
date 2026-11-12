package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"incidentctl/internal/config"
	"incidentctl/internal/exercises"
	"incidentctl/internal/identity"
	"incidentctl/internal/remote"
	"incidentctl/internal/verifier"
	"incidentctl/internal/vm"
	"incidentctl/internal/vmstate"
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
	warning    string
	err        error
}

type solutionStage int

const (
	solutionCheckCapabilities solutionStage = iota
	solutionFindVM
	solutionCreateVM
	solutionConnectSSH
	solutionPrepareAnsible
	solutionPrepareAudit
	solutionApplyInfra
	solutionApplyIncident
	solutionVerifyIncident
	solutionStartAudit
	solutionOpenTerminal
)

const solutionStageCount = int(solutionOpenTerminal) + 1

var solutionStageNames = [...]string{
	"Проверка токена",
	"Поиск сохранённой виртуальной машины",
	"Создание виртуальной машины",
	"Подключение к SSH",
	"Подготовка Ansible",
	"Установка audit-agent",
	"Применение infra",
	"Применение incident",
	"Проверка инцидента",
	"Начало записи действий",
	"Открытие веб-терминала",
}

var spinnerFrames = [...]string{"◐", "◓", "◑", "◒"}

type spinnerTickMsg struct{}

type solutionRun struct {
	provider      vm.APIProvider
	item          exerciseItem
	remoteWorkDir string
	state         *vmstate.Store
	instance      vm.VirtualMachine
	savedRecord   vmstate.Record
	connection    vm.Connection
	cleanup       func()
	client        *remote.Client
	output        bytes.Buffer
	terminalURL   string
	openBrowser   func(string) error
	audit         auditOptions
}

type solutionStageResult struct {
	run   *solutionRun
	stage solutionStage
	err   error
}

const (
	tokenExpiredMessage = "токен просрочен, обновите токен перед решением задач"
	serverBusyMessage   = "сейчас сервер занят, попробуйте подключиться позже"
)

type tuiModel struct {
	provider          vm.ConnectionProvider
	remoteWorkDir     string
	vmState           *vmstate.Store
	connection        *vm.Connection
	sshCleanup        func()
	solution          *solutionRun
	exercises         []exerciseItem
	screen            tuiScreen
	returnTo          tuiScreen
	tipsReturn        tuiScreen
	selected          int
	tipIndex          int
	scroll            int
	width             int
	height            int
	busy              bool
	status            string
	logs              string
	spinnerFrame      int
	solutionStage     solutionStage
	solutionStarted   time.Time
	solutionDurations [solutionStageCount]time.Duration
	solutionDone      [solutionStageCount]bool
	solutionFailed    bool
	solutionFailure   solutionStage
	solutionCanRetry  bool
	tokenFile         string
	token             string
	tokenInput        string
	tokenInstalledAt  int64
	tokenReturn       tuiScreen
	tokenError        string
	audit             auditOptions
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

func runTUI(source exercises.Source, provider vm.ConnectionProvider, remoteWorkDir, tokenFile string, audit auditOptions) error {
	loaded, err := source.List(context.Background())
	if err != nil {
		return err
	}
	state, err := vmstate.Load("")
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
		audit:         audit,
		remoteWorkDir: remoteWorkDir,
		vmState:       state,
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
	_, runErr := tea.NewProgram(model, tea.WithAltScreen()).Run()
	cleanupErr := cleanupVMs(context.Background(), provider)
	if cleanupErr == nil {
		cleanupErr = state.Clear()
	}
	return errors.Join(runErr, cleanupErr)
}

func cleanupVMs(ctx context.Context, provider vm.ConnectionProvider) error {
	apiProvider, ok := provider.(vm.APIProvider)
	if !ok {
		return nil
	}
	return apiProvider.CleanupAllVMs(ctx)
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
	case spinnerTickMsg:
		if !m.busy || m.screen != screenSolution {
			return m, nil
		}
		m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
		return m, spinnerTickCmd()
	case solutionStageResult:
		if msg.run != m.solution {
			return m, nil
		}
		if msg.err != nil {
			m.completeSolutionStage(msg.stage)
			m.solutionFailed = true
			m.solutionFailure = msg.stage
			var sshError *remote.CommandError
			m.solutionCanRetry = errors.As(msg.err, &sshError) && sshError.ExitCode == 255 && sshError.BeforeCommand &&
				(msg.stage == solutionPrepareAnsible || msg.stage == solutionApplyInfra || msg.stage == solutionApplyIncident)
			m.busy = false
			if msg.stage == solutionOpenTerminal || m.solutionCanRetry {
				m.adoptSolutionConnection(msg.run)
			} else if msg.run.cleanup != nil {
				msg.run.cleanup()
				msg.run.cleanup = nil
			}
			if errors.Is(msg.err, vm.ErrTokenExpired) {
				m.status = tokenExpiredMessage
			} else if msg.stage == solutionOpenTerminal {
				m.status = "Ошибка: " + msg.err.Error() + ". Нажмите p для повтора или o для SSH."
				if errors.Is(msg.err, vm.ErrBrowserPending) {
					m.status = "Веб-терминал Nexterm пока не готов. VM и запись действий готовы. Нажмите p для повтора или o для SSH."
				} else if errors.Is(msg.err, vm.ErrBrowserUnsupported) {
					m.status = "Веб-терминал недоступен для SSH-ключа этой VM. Нажмите o для SSH."
				}
			} else if msg.stage == solutionVerifyIncident && isSSHTransportFailure(msg.err) {
				m.status = "Ошибка SSH: потеряна связь с VM " + msg.run.instance.ID + ". Нажмите p, чтобы повторить проверку инцидента на той же VM."
			} else if msg.stage == solutionCheckCapabilities || msg.stage == solutionCreateVM || msg.stage == solutionConnectSSH {
				m.status = serverBusyMessage
			} else {
				m.status = "Ошибка: " + msg.err.Error()
			}
			if m.solutionCanRetry {
				m.status += ". Нажмите p, чтобы повторить этап на той же VM."
			}
			m.logs = msg.run.output.String()
			return m, nil
		}
		m.completeSolutionStage(msg.stage)
		switch msg.stage {
		case solutionCheckCapabilities:
			m.startSolutionStage(solutionFindVM, "Проверяю сохранённую виртуальную машину…")
			return m, msg.run.findVMCmd()
		case solutionFindVM:
			if msg.run.instance.ID != "" {
				m.startSolutionStage(solutionConnectSSH, "Подключаюсь к сохранённой виртуальной машине…")
				return m, msg.run.connectSSHCmd()
			}
			m.startSolutionStage(solutionCreateVM, "Создаю виртуальную машину…")
			return m, msg.run.createVMCmd()
		case solutionCreateVM:
			m.startSolutionStage(solutionConnectSSH, "Ожидаю готовность SSH…")
			return m, msg.run.connectSSHCmd()
		case solutionConnectSSH:
			m.startSolutionStage(solutionPrepareAnsible, "Устанавливаю всё необходимое для Ansible…")
			return m, msg.run.prepareAnsibleCmd()
		case solutionPrepareAnsible:
			m.startSolutionStage(solutionPrepareAudit, "Устанавливаю audit-agent…")
			return m, msg.run.prepareAuditCmd()
		case solutionPrepareAudit:
			m.startSolutionStage(solutionApplyInfra, "Применяю инфраструктурный playbook…")
			return m, msg.run.applyInfraCmd()
		case solutionApplyInfra:
			m.startSolutionStage(solutionApplyIncident, "Применяю incident playbook…")
			return m, msg.run.applyIncidentCmd()
		case solutionApplyIncident:
			m.startSolutionStage(solutionVerifyIncident, "Проверяю, что инцидент воспроизведён…")
			return m, msg.run.verifyIncidentCmd()
		case solutionVerifyIncident:
			m.startSolutionStage(solutionStartAudit, "Начинаю запись действий…")
			return m, msg.run.startAuditCmd()
		case solutionStartAudit:
			m.startSolutionStage(solutionOpenTerminal, "Инфраструктура подтверждена. Открываю веб-терминал…")
			return m, msg.run.openTerminalCmd()
		case solutionOpenTerminal:
			m.busy = false
			m.logs = msg.run.output.String()
			m.adoptSolutionConnection(msg.run)
			m.status = "Задача готова: запись действий включена, веб-терминал открыт."
		}
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
			if msg.kind == operationVerify && isRejectedVerification(msg.err) {
				m.status = "Решение пока не прошло проверку Ansible. Можно продолжить работу."
			} else {
				m.status = "Ошибка: " + msg.err.Error()
			}
			if msg.warning != "" {
				m.status += "\nПредупреждение: " + msg.warning
			}
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
			m.status = "Проверка решения прошла успешно. Запись событий включена."
		case operationOpenSSH:
			m.status = "SSH-сессия открыта в отдельном окне терминала."
		}
		if msg.warning != "" {
			m.status += "\nПредупреждение: " + msg.warning
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
		case "p":
			if m.solutionFailed && m.solutionCanRetry && m.solution != nil {
				return m.retryUnstartedStage()
			}
			if m.solutionFailed && m.solutionFailure == solutionOpenTerminal && m.solution != nil {
				return m.retryWebTerminal()
			}
			if m.solutionFailed && m.solutionFailure == solutionVerifyIncident && m.solution != nil {
				return m.retryIncidentCheck()
			}
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
	if m.vmState == nil {
		m.status = "Нельзя начать: не удалось загрузить состояние виртуальных машин"
		return m, nil
	}
	run := &solutionRun{
		audit:         m.audit,
		provider:      apiProvider,
		item:          item,
		remoteWorkDir: m.remoteWorkDir,
		state:         m.vmState,
	}
	m.releaseSSH()
	m.solution = run
	m.solutionDurations = [solutionStageCount]time.Duration{}
	m.solutionDone = [solutionStageCount]bool{}
	m.solutionFailed = false
	m.solutionCanRetry = false
	m.screen, m.busy, m.logs = screenSolution, true, ""
	m.startSolutionStage(solutionCheckCapabilities, "Проверяю токен…")
	return m, tea.Batch(run.checkCapabilitiesCmd(), spinnerTickCmd())
}

func spinnerTickCmd() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return spinnerTickMsg{} })
}

func (m *tuiModel) startSolutionStage(stage solutionStage, status string) {
	m.solutionStage = stage
	m.solutionStarted = time.Now()
	m.spinnerFrame = 0
	m.status = status
}

func (m *tuiModel) completeSolutionStage(stage solutionStage) {
	if stage < 0 || int(stage) >= solutionStageCount || m.solutionStarted.IsZero() {
		return
	}
	m.solutionDurations[stage] = time.Since(m.solutionStarted)
	m.solutionDone[stage] = true
}

func (r *solutionRun) checkCapabilitiesCmd() tea.Cmd {
	return func() tea.Msg {
		_, err := r.provider.Capabilities(context.Background())
		return solutionStageResult{run: r, stage: solutionCheckCapabilities, err: err}
	}
}

func (r *solutionRun) findVMCmd() tea.Cmd {
	return func() tea.Msg {
		record, ok := r.state.Lookup(r.item.exercise.Name)
		if !ok {
			return solutionStageResult{run: r, stage: solutionFindVM}
		}
		if strings.TrimSpace(record.VMID) == "" {
			return solutionStageResult{run: r, stage: solutionFindVM, err: fmt.Errorf("saved VM for exercise %q has no VM id", r.item.exercise.Name)}
		}
		r.savedRecord = record
		r.instance = vm.VirtualMachine{ID: record.VMID}
		return solutionStageResult{run: r, stage: solutionFindVM}
	}
}

func (r *solutionRun) createVMCmd() tea.Cmd {
	return func() tea.Msg {
		instance, err := r.provider.CreateVM(context.Background(), vm.RequestFromExercise(r.item.exercise))
		if err == nil {
			r.instance = instance
			err = r.state.SaveVM(r.item.exercise.Name, instance.ID)
			fmt.Fprintf(&r.output, "VM ID: %s\n", instance.ID)
		}
		return solutionStageResult{run: r, stage: solutionCreateVM, err: err}
	}
}

func (r *solutionRun) connectSSHCmd() tea.Cmd {
	return func() tea.Msg {
		var err error
		if r.savedRecord.SSH != nil {
			var connection vm.Connection
			var cleanup func()
			connection, cleanup, err = r.state.OpenConnection(r.item.exercise.Name)
			if err == nil {
				r.connection, r.cleanup = connection, cleanup
				r.client = remote.NewWithWriters(connection, &r.output, &r.output)
			}
		} else {
			err = r.refreshSSH()
		}
		return solutionStageResult{run: r, stage: solutionConnectSSH, err: err}
	}
}

func (r *solutionRun) refreshSSH() error {
	connection, cleanup, err := r.provider.SSHConnection(context.Background(), r.instance.ID)
	if err != nil {
		return err
	}
	if r.state != nil {
		if err := r.state.SaveSSH(r.item.exercise.Name, connection); err != nil {
			if cleanup != nil {
				cleanup()
			}
			return err
		}
	}
	if r.cleanup != nil {
		r.cleanup()
	}
	r.connection, r.cleanup = connection, cleanup
	r.client = remote.NewWithWriters(connection, &r.output, &r.output)
	return nil
}

func (m tuiModel) retryIncidentCheck() (tea.Model, tea.Cmd) {
	run := m.solution
	m.busy, m.solutionFailed = true, false
	m.solutionDone[solutionVerifyIncident] = false
	m.startSolutionStage(solutionVerifyIncident, "Восстанавливаю SSH и повторяю проверку инцидента…")
	return m, tea.Batch(func() tea.Msg {
		fmt.Fprintf(&run.output, "\nПовторная проверка инцидента на VM %s\n", run.instance.ID)
		if err := run.refreshSSH(); err != nil {
			return solutionStageResult{run: run, stage: solutionVerifyIncident, err: fmt.Errorf("refresh SSH metadata: %w", err)}
		}
		return run.verifyIncidentCmd()()
	}, spinnerTickCmd())
}

func (m tuiModel) retryUnstartedStage() (tea.Model, tea.Cmd) {
	stage := m.solutionFailure
	var command tea.Cmd
	switch stage {
	case solutionPrepareAnsible:
		command = m.solution.prepareAnsibleCmd()
	case solutionApplyInfra:
		command = m.solution.applyInfraCmd()
	case solutionApplyIncident:
		command = m.solution.applyIncidentCmd()
	default:
		return m, nil
	}
	m.busy, m.solutionFailed, m.solutionCanRetry = true, false, false
	m.solutionDone[stage] = false
	m.startSolutionStage(stage, "Повторяю подключение и незапущенный этап на той же VM…")
	return m, tea.Batch(command, spinnerTickCmd())
}

func isSSHTransportFailure(err error) bool {
	var commandErr *remote.CommandError
	return errors.As(err, &commandErr) && commandErr.ExitCode == 255
}

func (r *solutionRun) prepareAnsibleCmd() tea.Cmd {
	return func() tea.Msg {
		fmt.Fprintln(&r.output, "\n━━ Подготовка Ansible ━━")
		err := preparePlaybookHost(r.client, r.item.exercise)
		if err != nil {
			err = &playbookRunError{Phase: "prepare playbook host", Err: err}
		}
		return solutionStageResult{run: r, stage: solutionPrepareAnsible, err: err}
	}
}

func (r *solutionRun) applyInfraCmd() tea.Cmd {
	return r.applyPlaybookCmd(solutionApplyInfra, "Создание инфраструктуры", r.item.exercise.Playbooks.Infra)
}

func (r *solutionRun) prepareAuditCmd() tea.Cmd {
	return func() tea.Msg {
		err := r.audit.prepare(r.client, r.remoteWorkDir, r.item.exercise.Name)
		return solutionStageResult{run: r, stage: solutionPrepareAudit, err: err}
	}
}

func (r *solutionRun) startAuditCmd() tea.Cmd {
	return func() tea.Msg {
		err := r.audit.start(r.client, r.remoteWorkDir, r.item.exercise.Name)
		return solutionStageResult{run: r, stage: solutionStartAudit, err: err}
	}
}

func (r *solutionRun) applyIncidentCmd() tea.Cmd {
	return r.applyPlaybookCmd(solutionApplyIncident, "Внесение инцидента", r.item.exercise.Playbooks.Incident)
}

func (r *solutionRun) applyPlaybookCmd(stage solutionStage, title, playbook string) tea.Cmd {
	return func() tea.Msg {
		fmt.Fprintf(&r.output, "\n━━ %s ━━\n", title)
		err := runPlaybook(r.client, r.remoteWorkDir, r.item.exercise, r.item.dir, playbook, nil)
		return solutionStageResult{run: r, stage: stage, err: err}
	}
}

func (r *solutionRun) verifyIncidentCmd() tea.Cmd {
	return func() tea.Msg {
		fmt.Fprintln(&r.output, "\n━━ Проверка воспроизведения инцидента ━━")
		// Same engine as final verification: only an explicit failed check proves
		// the incident; technical errors never do.
		_, err := verifyExercise(context.Background(), r.client, r.remoteWorkDir, r.item.exercise, r.item.dir, nil, &r.output)
		if err == nil {
			return solutionStageResult{run: r, stage: solutionVerifyIncident, err: errors.New("verification неожиданно прошла после внесения инцидента")}
		}
		if !isExpectedVerificationFailure(err) {
			return solutionStageResult{run: r, stage: solutionVerifyIncident, err: fmt.Errorf("не удалось подтвердить ошибку инфраструктуры: %w", err)}
		}
		fmt.Fprintln(&r.output, "verification обнаружила неисправность — ожидаемый результат.")
		return solutionStageResult{run: r, stage: solutionVerifyIncident}
	}
}

func (r *solutionRun) openTerminalCmd() tea.Cmd {
	return func() tea.Msg {
		fmt.Fprintf(&r.output, "\n━━ Открытие веб-терминала · VM %s ━━\n", r.instance.ID)
		if r.terminalURL == "" {
			terminalURL, err := r.provider.TerminalURL(context.Background(), r.instance.ID)
			if err != nil {
				err = fmt.Errorf("получение веб-терминала через VM API: %w", err)
				fmt.Fprintln(&r.output, "Ошибка:", err)
				return solutionStageResult{run: r, stage: solutionOpenTerminal, err: err}
			}
			r.terminalURL = terminalURL
		}
		fmt.Fprintln(&r.output, "Ссылка веб-терминала получена. Запускаю браузер.")
		open := r.openBrowser
		if open == nil {
			open = openBrowserURL
		}
		if err := open(r.terminalURL); err != nil {
			err = fmt.Errorf("запуск браузера: %w", err)
			fmt.Fprintln(&r.output, "Ошибка:", err)
			return solutionStageResult{run: r, stage: solutionOpenTerminal, err: err}
		}
		fmt.Fprintln(&r.output, "Веб-терминал открыт.")
		return solutionStageResult{run: r, stage: solutionOpenTerminal}
	}
}

func (m tuiModel) retryWebTerminal() (tea.Model, tea.Cmd) {
	m.busy, m.solutionFailed = true, false
	m.solutionDone[solutionOpenTerminal] = false
	m.startSolutionStage(solutionOpenTerminal, "Повторно открываю веб-терминал на той же VM…")
	return m, tea.Batch(m.solution.openTerminalCmd(), spinnerTickCmd())
}

func (m *tuiModel) adoptSolutionConnection(run *solutionRun) {
	if run.cleanup == nil {
		return
	}
	m.releaseSSH()
	m.connection = &run.connection
	m.sshCleanup = run.cleanup
	run.cleanup = nil
}

func (m *tuiModel) releaseSSH() {
	if m.sshCleanup != nil {
		m.sshCleanup()
		m.sshCleanup = nil
	}
	m.connection = nil
}

func isExpectedVerificationFailure(err error) bool {
	if isVerdict(err) {
		return true
	}
	if isTechnicalVerification(err) {
		return false
	}
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
	item, connection, remoteWorkDir, audit := m.current(), *m.connection, m.remoteWorkDir, m.audit
	m.busy, m.status = true, "Проверяю решение…"
	return m, func() tea.Msg {
		var output bytes.Buffer
		client := remote.NewWithWriters(connection, &output, &output)
		var warning string
		audit.OnWarning = func(message string) { warning = message }
		err := audit.verifyResult(client, remoteWorkDir, item.exercise.Name, item.dir, &output, func(out io.Writer) (*verifier.Result, error) {
			return verifyExercise(context.Background(), remote.NewWithWriters(connection, out, out), remoteWorkDir, item.exercise, item.dir, nil, out)
		})
		return operationResult{kind: operationVerify, output: output.String(), warning: warning, err: err}
	}
}

// Ansible uses 2 when a task failed; transport, upload and execution failures
// remain technical errors and must not be presented as a rejected solution.
func isRejectedVerification(err error) bool {
	var resumeErr *recordingResumeError
	if errors.As(err, &resumeErr) {
		return false
	}
	if isVerdict(err) {
		return true
	}
	if isTechnicalVerification(err) {
		return false
	}
	var runErr *playbookRunError
	var commandErr *remote.CommandError
	return errors.As(err, &runErr) && runErr.Phase == "execute playbook" &&
		errors.As(err, &commandErr) && commandErr.ExitCode == 2
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
	if proxy, err := remote.ProxyOption(); err == nil && proxy != "" {
		args = append(args, "-o", proxy)
	}
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
	return localSSHCommand(args)
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
		return exec.Command("cmd", "/c", "start", "", "powershell", "-NoExit", "-Command", command).Run()
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
		state = spinnerFrames[m.spinnerFrame] + " " + state
	} else if strings.HasPrefix(state, "Ошибка") || strings.HasPrefix(state, "Решение пока не прошло") {
		state = errorStyle.Render(state)
	} else if state != "" {
		state = successStyle.Render(state)
	}
	if state == "" {
		state = mutedStyle.Render("Решение активно.")
	}
	logHeight := max(4, min(10, m.height-27))
	logs := tailLines(m.logs, logHeight)
	if logs == "" {
		logs = mutedStyle.Render("Вывод команд появится здесь.")
	}
	content := titleStyle.Render("Решение · "+item.exercise.Name) + "\n\n" + state + "\n\n"
	content += m.viewSolutionStages() + "\n"
	content += mutedStyle.Render("Последний вывод") + "\n" + logs + "\n\n"
	content += mutedStyle.Render("d описание   t подсказки   v проверить   a токен   x выйти из решения   q выйти")
	if m.solutionFailed && m.solutionFailure == solutionVerifyIncident {
		content += "\n" + mutedStyle.Render("p повторить проверку инцидента на той же VM")
	}
	if m.solutionFailed && m.solutionFailure == solutionOpenTerminal {
		content += "\n" + mutedStyle.Render("p повторить открытие веб-терминала   o открыть SSH-терминал   c команда SSH")
	}
	if m.solutionFailed && m.solutionCanRetry {
		content += "\n" + mutedStyle.Render("p повторить незапущенный этап на той же VM")
	}
	return panelStyle.Width(max(30, width-4)).Render(content)
}

func (m tuiModel) viewSolutionStages() string {
	var b strings.Builder
	b.WriteString(mutedStyle.Render("Этапы") + "\n")
	for index, name := range solutionStageNames {
		stage := solutionStage(index)
		symbol := "·"
		lineStyle := mutedStyle
		duration := "—"
		if m.solutionFailed && m.solutionFailure == stage {
			duration = formatStageDuration(m.solutionDurations[index])
			symbol = "✗"
			lineStyle = errorStyle
		} else if m.solutionDone[index] {
			duration = formatStageDuration(m.solutionDurations[index])
			symbol = "✓"
			lineStyle = successStyle
		} else if m.busy && m.solutionStage == stage {
			symbol = spinnerFrames[m.spinnerFrame]
			duration = formatStageDuration(time.Since(m.solutionStarted))
			lineStyle = titleStyle
		}
		b.WriteString(lineStyle.Render(fmt.Sprintf("%s %-34s %s", symbol, name, duration)) + "\n")
	}
	return b.String()
}

func formatStageDuration(duration time.Duration) string {
	if duration < time.Second {
		return fmt.Sprintf("%.1fs", duration.Seconds())
	}
	minutes := int(duration / time.Minute)
	seconds := int(duration/time.Second) % 60
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
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
