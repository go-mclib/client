package client

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

// TUI represents the terminal user interface for interactive mode
type TUI struct {
	client       *Client
	viewport     viewport.Model
	textInput    textinput.Model
	logs         []string
	logMutex     sync.Mutex
	ready        bool
	inputEnabled bool
	width        int
	height       int
	quitting     bool
}

// NewTUI creates a new TUI instance
func NewTUI(client *Client) *TUI {
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
			t.quitting = true
			t.client.Disconnect()
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

	case logMsg:
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

	case enableInputMsg:
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
	if t.quitting {
		return "Goodbye!\n"
	}

	if !t.ready {
		return "Initializing..."
	}


	title := titleStyle.Render(fmt.Sprintf("Minecraft Client - %s@%s:%d", t.client.Username, t.client.Host, t.client.Port))

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
	if t.client.MaxLogLines > 0 && len(t.logs) > t.client.MaxLogLines {
		t.logs = t.logs[len(t.logs)-t.client.MaxLogLines:]
	}
}

func (t *TUI) renderLogs() string {
	t.logMutex.Lock()
	defer t.logMutex.Unlock()
	return strings.Join(t.logs, "\n")
}

type logMsg string
type enableInputMsg struct{}

// TUIWriter is an io.Writer that sends output to the TUI
type TUIWriter struct {
	program *tea.Program
}

// Write implements io.Writer
func (w *TUIWriter) Write(p []byte) (n int, err error) {
	msg := strings.TrimSuffix(string(p), "\n")
	if msg != "" {
		w.program.Send(logMsg(msg))
	}
	return len(p), nil
}

// StartTUI starts the TUI and returns a writer for logging
func (c *Client) StartTUI() (*tea.Program, io.Writer) {
	tui := NewTUI(c)
	p := tea.NewProgram(tui, tea.WithAltScreen())
	writer := &TUIWriter{program: p}
	return p, writer
}

// EnableInput enables the chat input in the TUI
func (c *Client) EnableInput() {
	if c.tuiProgram != nil {
		c.tuiProgram.Send(enableInputMsg{})
	}
}
