package tui

import (
	"fmt"
	"io"
	"strings"
	"sync"

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

// ClientInterface defines the methods required from a client for TUI interaction
type ClientInterface interface {
	GetUsername() string
	GetAddress() string
	GetMaxLogLines() int
	SendChatMessage(msg string) error
	SendCommand(cmd string) error
	Disconnect(force bool) error
}

// TUI represents the terminal user interface for interactive mode
type TUI struct {
	client       ClientInterface
	viewport     viewport.Model
	textInput    textinput.Model
	logs         []string
	logMutex     sync.Mutex
	ready        bool
	inputEnabled bool
	width        int
	height       int
}

// New creates a new TUI instance
func New(client ClientInterface) *TUI {
	ti := textinput.New()
	ti.Placeholder = "Waiting for player to spawn..."
	ti.Blur() // start unfocused
	ti.CharLimit = 256
	ti.Width = 50

	return &TUI{
		client:       client,
		textInput:    ti,
		logs:         []string{},
		inputEnabled: false,
	}
}

// Init initializes the TUI
func (t *TUI) Init() tea.Cmd {
	return textinput.Blink
}

// Update handles TUI updates
func (t *TUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		cmd  tea.Cmd
		cmds []tea.Cmd
	)

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			t.client.Disconnect(true)
			return t, tea.Quit

		case tea.KeyEnter:
			if !t.inputEnabled {
				return t, nil
			}
			input := strings.TrimSpace(t.textInput.Value())
			if input != "" {
				if strings.HasPrefix(input, "/") {
					// cmd
					if err := t.client.SendCommand(input); err != nil {
						t.AddLog(fmt.Sprintf("Error sending command: %v", err))
					} else {
						t.AddLog(fmt.Sprintf("cmd > %s", input))
					}
				} else {
					// chat msg
					if err := t.client.SendChatMessage(input); err != nil {
						t.AddLog(fmt.Sprintf("Error sending message: %v", err))
					}
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
		t.width = msg.Width
		t.height = msg.Height
		t.textInput.Width = msg.Width - 2

	case LogMsg:
		t.AddLog(string(msg))
		if t.ready {
			// do not scroll if not at bottom, to prevent flickering
			wasAtBottom := t.viewport.AtBottom()
			t.viewport.SetContent(t.renderLogs())
			if wasAtBottom {
				t.viewport.GotoBottom()
			}
		}
		return t, nil

	case EnableInputMsg:
		t.inputEnabled = true
		t.textInput.Placeholder = "Type a message or /command..."
		t.textInput.Focus()
		return t, nil
	}

	// update viewport
	if t.ready {
		t.viewport, cmd = t.viewport.Update(msg)
		cmds = append(cmds, cmd)
	}

	// update text input (only if enabled)
	if t.inputEnabled {
		t.textInput, cmd = t.textInput.Update(msg)
		cmds = append(cmds, cmd)
	}

	return t, tea.Batch(cmds...)
}

// View renders the TUI
func (t *TUI) View() string {
	if !t.ready {
		return "Initializing..."
	}

	title := titleStyle.Render(fmt.Sprintf("Minecraft Client - %s@%s", t.client.GetUsername(), t.client.GetAddress()))

	var helpText string
	if t.inputEnabled {
		helpText = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Render("Enter: send • Ctrl+C/Esc: quit")
	} else {
		helpText = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Render("Waiting for player to spawn... • Ctrl+C/Esc: quit")
	}

	return fmt.Sprintf(
		"%s\n%s\n%s\n%s",
		title,
		t.viewport.View(),
		inputStyle.Render("> "+t.textInput.View()),
		helpText,
	)
}

// AddLog adds a log message to the TUI
func (t *TUI) AddLog(msg string) {
	t.logMutex.Lock()
	defer t.logMutex.Unlock()
	t.logs = append(t.logs, msg)

	// trim logs
	maxLines := t.client.GetMaxLogLines()
	if maxLines > 0 && len(t.logs) > maxLines {
		t.logs = t.logs[len(t.logs)-maxLines:]
	}
}

func (t *TUI) renderLogs() string {
	t.logMutex.Lock()
	defer t.logMutex.Unlock()
	return strings.Join(t.logs, "\n")
}

// LogMsg is a message type for logging
type LogMsg string

// EnableInputMsg is a message type to enable input
type EnableInputMsg struct{}

// Writer is an io.Writer that sends output to the TUI
type Writer struct {
	program *tea.Program
}

// NewWriter creates a new TUI Writer
func NewWriter(program *tea.Program) *Writer {
	return &Writer{program: program}
}

// Write implements io.Writer
func (w *Writer) Write(p []byte) (n int, err error) {
	msg := strings.TrimSuffix(string(p), "\n")
	if msg != "" {
		w.program.Send(LogMsg(msg))
	}
	return len(p), nil
}

// Start creates and starts a new TUI program, returning the program and a writer for logging
func Start(client ClientInterface) (*tea.Program, io.Writer) {
	t := New(client)
	p := tea.NewProgram(t, tea.WithAltScreen())
	writer := NewWriter(p)
	return p, writer
}

// EnableInput sends an enable input message to the given program
func EnableInput(program *tea.Program) {
	if program != nil {
		program.Send(EnableInputMsg{})
	}
}
