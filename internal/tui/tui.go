// Package tui is the terminal UI used by the client's interactive mode.
package tui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("39"))

	inputStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("205"))
)

// Config carries the display data and actions the TUI needs from the client.
type Config struct {
	Username    string
	Address     string
	MaxLogLines int // 0 keeps every line

	SendChat    func(msg string) error
	SendCommand func(cmd string) error
	// Quit is called when the user exits with Ctrl+C or Esc.
	Quit func()
}

// Program is a running TUI.
type Program struct {
	program *tea.Program
}

// New creates the TUI program. Use Writer for log output and Run to start it.
func New(cfg Config) *Program {
	return &Program{program: tea.NewProgram(newModel(cfg), tea.WithAltScreen())}
}

// Run blocks until the TUI exits.
func (p *Program) Run() error {
	_, err := p.program.Run()
	return err
}

// Quit stops the TUI.
func (p *Program) Quit() { p.program.Quit() }

// EnableInput unlocks the text input once the player has joined.
func (p *Program) EnableInput() { p.program.Send(enableInputMsg{}) }

// Writer returns an io.Writer that appends each written line to the log view.
func (p *Program) Writer() io.Writer { return logWriter{program: p.program} }

type model struct {
	cfg          Config
	viewport     viewport.Model
	textInput    textinput.Model
	logs         []string
	ready        bool
	inputEnabled bool
}

func newModel(cfg Config) *model {
	ti := textinput.New()
	ti.Placeholder = "Waiting to join..."
	ti.Blur() // start unfocused
	ti.CharLimit = 256
	ti.Width = 50

	return &model{cfg: cfg, textInput: ti}
}

// Init, Update and View implement bubbletea's tea.Model. bubbletea calls them
// from its single event loop, so the model needs no locking.
func (t *model) Init() tea.Cmd {
	return textinput.Blink
}

func (t *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		cmd  tea.Cmd
		cmds []tea.Cmd
	)

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			t.cfg.Quit()
			return t, tea.Quit

		case tea.KeyEnter:
			if !t.inputEnabled {
				return t, nil
			}
			input := strings.TrimSpace(t.textInput.Value())
			if input != "" {
				if strings.HasPrefix(input, "/") {
					if err := t.cfg.SendCommand(input); err != nil {
						t.addLog(fmt.Sprintf("Error sending command: %v", err))
					} else {
						t.addLog(fmt.Sprintf("cmd > %s", input))
					}
				} else if err := t.cfg.SendChat(input); err != nil {
					t.addLog(fmt.Sprintf("Error sending message: %v", err))
				}
				t.textInput.SetValue("")
			}
			return t, nil
		}

	case tea.WindowSizeMsg:
		if !t.ready {
			t.viewport = viewport.New(msg.Width, msg.Height-3)
			t.viewport.SetContent(t.renderLogs())
			t.ready = true
		} else {
			t.viewport.Width = msg.Width
			t.viewport.Height = msg.Height - 3
		}
		t.textInput.Width = msg.Width - 2

	case logMsg:
		t.addLog(string(msg))
		if t.ready {
			// do not scroll if not at bottom, to prevent flickering
			wasAtBottom := t.viewport.AtBottom()
			t.viewport.SetContent(t.renderLogs())
			if wasAtBottom {
				t.viewport.GotoBottom()
			}
		}
		return t, nil

	case enableInputMsg:
		t.inputEnabled = true
		t.textInput.Placeholder = "Type a message or /command..."
		t.textInput.Focus()
		return t, nil
	}

	if t.ready {
		t.viewport, cmd = t.viewport.Update(msg)
		cmds = append(cmds, cmd)
	}

	if t.inputEnabled {
		t.textInput, cmd = t.textInput.Update(msg)
		cmds = append(cmds, cmd)
	}

	return t, tea.Batch(cmds...)
}

func (t *model) View() string {
	if !t.ready {
		return "Initializing..."
	}

	title := titleStyle.Render(fmt.Sprintf("Minecraft Client - %s@%s", t.cfg.Username, t.cfg.Address))

	help := "Waiting to join... • Ctrl+C/Esc: quit"
	if t.inputEnabled {
		help = "Enter: send • Ctrl+C/Esc: quit"
	}
	helpText := lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render(help)

	return fmt.Sprintf(
		"%s\n%s\n%s\n%s",
		title,
		t.viewport.View(),
		inputStyle.Render("> "+t.textInput.View()),
		helpText,
	)
}

// addLog appends a log line, trimming to MaxLogLines.
func (t *model) addLog(msg string) {
	t.logs = append(t.logs, msg)
	if maxLines := t.cfg.MaxLogLines; maxLines > 0 && len(t.logs) > maxLines {
		t.logs = t.logs[len(t.logs)-maxLines:]
	}
}

func (t *model) renderLogs() string {
	return strings.Join(t.logs, "\n")
}

type logMsg string

type enableInputMsg struct{}

type logWriter struct {
	program *tea.Program
}

func (w logWriter) Write(p []byte) (n int, err error) {
	if msg := strings.TrimSuffix(string(p), "\n"); msg != "" {
		w.program.Send(logMsg(msg))
	}
	return len(p), nil
}
