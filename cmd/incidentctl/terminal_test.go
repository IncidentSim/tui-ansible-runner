package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"incidentctl/internal/vm"
)

type terminalTestProvider struct {
	vm.APIProvider
	err   error
	url   string
	calls int
	vmID  string
}

func (p *terminalTestProvider) TerminalURL(_ context.Context, id string) (string, error) {
	p.calls++
	p.vmID = id
	return p.url, p.err
}

func TestTerminalFailureShowsCauseAndKeepsSSH(t *testing.T) {
	p := &terminalTestProvider{err: errors.New("terminal operation op-123 timed out")}
	cleaned := false
	run := &solutionRun{provider: p, instance: vm.VirtualMachine{ID: "existing-vm"}, connection: vm.Connection{Host: "vm.example"}, cleanup: func() { cleaned = true }}
	model := tuiModel{screen: screenSolution, solution: run, busy: true}
	result := run.openTerminalCmd()()
	updated, _ := model.Update(result)
	model = updated.(tuiModel)
	if model.status == serverBusyMessage || !strings.Contains(model.status, "op-123 timed out") || !strings.Contains(model.logs, "op-123 timed out") {
		t.Fatalf("terminal cause was hidden: %s\n%s", model.status, model.logs)
	}
	if model.connection == nil || model.sshCleanup == nil || cleaned {
		t.Fatal("terminal failure must retain the prepared VM's SSH access")
	}
	if !strings.Contains(model.viewSolution(150), "p повторить открытие веб-терминала") {
		t.Fatal("missing terminal retry hint")
	}
}

func TestTerminalPendingExplainsBrowserReadiness(t *testing.T) {
	p := &terminalTestProvider{err: vm.ErrBrowserPending}
	run := &solutionRun{provider: p, instance: vm.VirtualMachine{ID: "existing-vm"}}
	model := tuiModel{screen: screenSolution, solution: run}
	updated, _ := model.Update(run.openTerminalCmd()())
	status := updated.(tuiModel).status
	if status == serverBusyMessage || !strings.Contains(status, "Nexterm пока не готов") || !strings.Contains(status, "o для SSH") {
		t.Fatalf("wrong pending status: %s", status)
	}
}

func TestTerminalRetryKeepsPreparedVMAndRecording(t *testing.T) {
	p := &terminalTestProvider{url: "https://terminal.example/session"}
	run := &solutionRun{provider: p, instance: vm.VirtualMachine{ID: "existing-vm"}, openBrowser: func(string) error { return nil }}
	model := tuiModel{screen: screenSolution, solution: run, solutionFailed: true, solutionFailure: solutionOpenTerminal}
	for stage := solutionStage(0); stage < solutionOpenTerminal; stage++ {
		model.solutionDone[stage] = true
	}
	updated, cmd := model.updateKey("p")
	retrying := updated.(tuiModel)
	if !retrying.busy || retrying.solutionFailed || retrying.solutionStage != solutionOpenTerminal {
		t.Fatal("retry must restart only the terminal stage")
	}
	for stage := solutionStage(0); stage < solutionOpenTerminal; stage++ {
		if !retrying.solutionDone[stage] {
			t.Fatalf("retry reset stage %v", stage)
		}
	}
	commands := cmd().(tea.BatchMsg)
	result := commands[0]().(solutionStageResult)
	if result.err != nil || p.calls != 1 || p.vmID != "existing-vm" {
		t.Fatalf("terminal retry: %v, calls %d, VM %s", result.err, p.calls, p.vmID)
	}
}

func TestBrowserFailureRetainsURLPrivatelyAndRetryDoesNotCallAPI(t *testing.T) {
	p := &terminalTestProvider{url: "https://terminal.example/session"}
	browserCalls := 0
	run := &solutionRun{provider: p, instance: vm.VirtualMachine{ID: "existing-vm"}, openBrowser: func(url string) error {
		browserCalls++
		if url != p.url {
			t.Fatalf("wrong URL %s", url)
		}
		if browserCalls == 1 {
			return errors.New("browser unavailable")
		}
		return nil
	}}
	first := run.openTerminalCmd()().(solutionStageResult)
	if first.err == nil || !strings.Contains(first.err.Error(), "запуск браузера") || run.terminalURL != p.url || strings.Contains(run.output.String(), p.url) {
		t.Fatal("browser error must retain its session URL without logging it")
	}
	second := run.openTerminalCmd()().(solutionStageResult)
	if second.err != nil || p.calls != 1 || browserCalls != 2 {
		t.Fatalf("browser retry requested a new terminal: %v, API %d, browser %d", second.err, p.calls, browserCalls)
	}
}
