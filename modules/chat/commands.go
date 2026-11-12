package chat

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/go-mclib/data/packets"
	"github.com/go-mclib/data/registries"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

// commandTree is the validated server command graph; children, redirects
// and root index nodes.
type commandTree struct {
	nodes []packets.CommandNode
	root  int
}
type signableArgument struct{ name, text string }

// Commands returns sorted root literals from the permission-filtered server graph.
// The graph snapshot is safe concurrently with HandlePacket and lifecycle reset.
func (m *Module) Commands() ([]string, error) {
	m.commandMu.RLock()
	defer m.commandMu.RUnlock()
	if m.commandTreeError != nil {
		return nil, m.commandTreeError
	}
	if m.commandTree == nil {
		return nil, errors.New("server has not advertised its command tree; try /help")
	}
	names := make([]string, 0, len(m.commandTree.nodes[m.commandTree.root].Children))
	for _, id := range m.commandTree.nodes[m.commandTree.root].Children {
		n := &m.commandTree.nodes[id]
		if n.Type() == packets.CommandNodeLiteral {
			names = append(names, string(n.Name))
		}
	}
	slices.Sort(names)
	return names, nil
}

// commandParserMessage is the minecraft:message parser ID, the only argument
// whose text is signed.
func commandParserMessage() ns.VarInt {
	return ns.VarInt(registries.CommandArgumentType.Get("minecraft:message"))
}

func validCommandInput(command string) error {
	if len(command) > 32767 || command == "" || strings.HasPrefix(command, "/") {
		return errors.New("invalid command length or leading slash")
	}
	return validChatLine(command)
}
func (t *commandTree) hasSignable(id int, seen map[int]bool) bool {
	if seen[id] {
		return false
	}
	seen[id] = true
	n := &t.nodes[id]
	if n.Type() == packets.CommandNodeArgument && n.ParserID == commandParserMessage() {
		return true
	}
	if n.Flags&packets.CommandNodeRedirect != 0 && int(n.Redirect) != t.root && t.hasSignable(int(n.Redirect), seen) {
		return true
	}
	for _, child := range n.Children {
		if t.hasSignable(int(child), seen) {
			return true
		}
	}
	return false
}
func (t *commandTree) arguments(command string, allowSelectors bool) ([]signableArgument, error) {
	rootWord, _, _ := strings.Cut(command, " ")
	for _, id := range t.nodes[t.root].Children {
		n := &t.nodes[id]
		if n.Type() != packets.CommandNodeLiteral || string(n.Name) != rootWord {
			continue
		}
		if !t.hasSignable(int(id), make(map[int]bool)) {
			return nil, nil
		}
		steps := 0
		args, err := t.walk(int(id), command, len(rootWord), nil, &steps, allowSelectors)
		if err != nil {
			return nil, fmt.Errorf("cannot safely sign advertised command syntax: %w", err)
		}
		return args, nil
	}
	// Plugins may intentionally omit commands from the advertised tree. Such
	// commands have no client-side signable argument definition.
	return nil, nil
}

// walk follows Brigadier's single separator and non-root redirect contexts.
// It deliberately rejects unsupported or ambiguous parsers rather than guessing
// a successful branch: an extra signature breaks the server's message chain.
func (t *commandTree) walk(id int, input string, pos int, args []signableArgument, steps *int, allowSelectors bool) ([]signableArgument, error) {
	*steps++
	if *steps > 4096 {
		return nil, errors.New("command graph traversal limit")
	}
	n := &t.nodes[id]
	if pos == len(input) {
		if n.Flags&packets.CommandNodeExecutable != 0 {
			return args, nil
		}
		return nil, errors.New("incomplete command")
	}
	if input[pos] != ' ' {
		return nil, errors.New("expected argument separator")
	}
	pos++
	redirect := n.Flags&packets.CommandNodeRedirect != 0
	if redirect && int(n.Redirect) == t.root {
		// ArgumentVisitor(rejectRootRedirects=true) never visits this child
		// context, including any further aliases or nested root redirects.
		return args, nil
	}
	if redirect {
		n = &t.nodes[n.Redirect]
	}
	wordEnd := pos
	for wordEnd < len(input) && input[wordEnd] != ' ' {
		wordEnd++
	}
	for _, child := range n.Children {
		c := &t.nodes[child]
		if c.Type() == packets.CommandNodeLiteral && string(c.Name) == input[pos:wordEnd] {
			return t.walk(int(child), input, wordEnd, args, steps, allowSelectors)
		}
	}
	argument := -1
	for _, child := range n.Children {
		if t.nodes[child].Type() == packets.CommandNodeArgument {
			if argument >= 0 {
				return nil, errors.New("unsupported competing argument branches")
			}
			argument = int(child)
		}
	}
	if argument < 0 || pos == len(input) {
		return nil, errors.New("invalid or incomplete command")
	}
	c := &t.nodes[argument]
	end, err := commandArgumentEnd(c, input, pos, allowSelectors)
	if err != nil {
		return nil, fmt.Errorf("argument %s: %w", c.Name, err)
	}
	if c.ParserID == commandParserMessage() {
		if utf16Len(string(c.Name)) > 16 || len(args) >= 8 {
			return nil, errors.New("signature name or count exceeds protocol limit")
		}
		args = append(args, signableArgument{string(c.Name), input[pos:end]})
	}
	return t.walk(argument, input, end, args, steps, allowSelectors)
}

// Only parsers whose acceptance and span we can establish are used before a
// signed descendant. Other commands still use the ordinary unsigned path.
func commandArgumentEnd(n *packets.CommandNode, input string, start int, allowSelectors bool) (int, error) {
	switch registries.CommandArgumentType.ByID(int32(n.ParserID)) {
	case "minecraft:message":
		if utf16Len(input[start:]) > 256 {
			return 0, errors.New("message exceeds 256 UTF-16 units")
		}
		// MessageArgument ignores missing/unknown selector types, but propagates
		// every other selector syntax error, even inside otherwise plain text.
		for pos := start; pos < len(input); pos++ {
			if input[pos] == '@' && pos+1 < len(input) && strings.ContainsRune("aenprs", rune(input[pos+1])) {
				end, err := commandEntityEnd(input, pos, 0)
				if err != nil {
					return 0, err
				}
				pos = end - 1
			}
		}
		return len(input), nil
	case "minecraft:entity":
		if !allowSelectors && input[start] == '@' {
			return 0, errors.New("entity selector permission is not established; use a player name")
		}
		return commandEntityEnd(input, start, n.Properties.Flags)
	case "brigadier:string":
		if n.Properties.StringMode == 2 {
			return len(input), nil
		}
		end, _, err := commandString(input, start, n.Properties.StringMode == 1)
		if end == start && err == nil {
			err = errors.New("expected string")
		}
		return end, err
	case "brigadier:bool":
		end, token, err := commandString(input, start, false)
		if err == nil && token != "true" && token != "false" {
			err = errors.New("expected boolean")
		}
		return end, err
	default:
		return 0, fmt.Errorf("unsupported parser ID %d before signed message", n.ParserID)
	}
}

// Brigadier StringReader.readString/readUnquotedString, including its restricted
// escape set. Byte cursors retain the original UTF-8 spans for signing.
func commandString(input string, pos int, quoted bool) (int, string, error) {
	start := pos
	if quoted && pos < len(input) && (input[pos] == '"' || input[pos] == '\'') {
		quote := input[pos]
		pos++
		var value strings.Builder
		for pos < len(input) {
			c := input[pos]
			pos++
			if c == quote {
				return pos, value.String(), nil
			}
			if c == '\\' {
				if pos == len(input) || input[pos] != quote && input[pos] != '\\' {
					return 0, "", errors.New("invalid quoted string escape")
				}
				c = input[pos]
				pos++
			}
			value.WriteByte(c)
		}
		return 0, "", errors.New("unterminated quoted string")
	}
	for pos < len(input) {
		c := input[pos]
		if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_' || c == '-' || c == '.' || c == '+') {
			break
		}
		pos++
	}
	return pos, input[start:pos], nil
}

func commandSkipSpaces(input string, pos int) int {
	for pos < len(input) && input[pos] == ' ' {
		pos++
	}
	return pos
}

// UUID.fromString accepts shortened hexadecimal groups too. Restrict to canonical
// UUIDs rather than silently interpreting an unfamiliar spelling as a player name.
func commandUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if s[i] != '-' {
				return false
			}
		} else if !(s[i] >= '0' && s[i] <= '9' || s[i] >= 'a' && s[i] <= 'f' || s[i] >= 'A' && s[i] <= 'F') {
			return false
		}
	}
	return true
}

func commandEntityEnd(input string, start int, flags byte) (int, error) {
	if start >= len(input) {
		return 0, errors.New("expected entity")
	}
	if input[start] != '@' {
		end, name, err := commandString(input, start, true)
		if err != nil {
			return 0, err
		}
		uuid := commandUUID(name)
		// Noncanonical UUID forms can change includesEntities in Java. Reject
		// these even when short enough to otherwise be treated as player names.
		if !uuid && strings.Count(name, "-") == 4 {
			return 0, errors.New("unsupported noncanonical UUID target")
		}
		if !uuid && (name == "" || utf16Len(name) > 16) {
			return 0, errors.New("invalid player name or UUID")
		}
		if uuid && flags&packets.CommandEntityPlayersOnly != 0 {
			return 0, errors.New("UUID includes entities in a players-only argument")
		}
		return end, nil
	}
	if start+1 == len(input) || !strings.ContainsRune("aenprs", rune(input[start+1])) {
		return 0, errors.New("invalid entity selector type")
	}
	kind := input[start+1]
	single := kind != 'a' && kind != 'e'
	entities := kind == 'e' || kind == 'n' || kind == 's'
	pos := start + 2
	// Values: 1 = positive/set-once, 2 = inverted set. This mirrors
	// InvertableSetOptionState, not merely balanced brackets.
	options := make(map[string]byte)
	if pos < len(input) && input[pos] == '[' {
		pos = commandSkipSpaces(input, pos+1)
		for pos < len(input) && input[pos] != ']' {
			pos = commandSkipSpaces(input, pos)
			end, key, err := commandString(input, pos, true)
			if err != nil {
				return 0, err
			}
			pos = commandSkipSpaces(input, end)
			if pos == len(input) || input[pos] != '=' {
				return 0, errors.New("expected selector option value")
			}
			pos = commandSkipSpaces(input, pos+1)
			invertible := key == "name" || key == "team" || key == "tag" || key == "gamemode" || key == "type"
			inverted := invertible && pos < len(input) && input[pos] == '!'
			if inverted {
				pos = commandSkipSpaces(input, pos+1)
			}
			if key != "tag" && (options[key] == 1 || options[key] == 2 && !inverted) {
				return 0, fmt.Errorf("inapplicable repeated selector option %s", key)
			}
			switch key {
			case "name", "team", "tag", "gamemode", "sort":
				var value string
				pos, value, err = commandString(input, pos, key == "name")
				if err != nil {
					return 0, err
				}
				if key == "gamemode" {
					if value != "survival" && value != "creative" && value != "adventure" && value != "spectator" {
						return 0, errors.New("invalid selector gamemode")
					}
					entities = false
				}
				if key == "sort" && (kind == 's' || value != "nearest" && value != "furthest" && value != "random" && value != "arbitrary") {
					return 0, errors.New("invalid or inapplicable selector sort")
				}
			case "type":
				begin := pos
				for pos < len(input) && (input[pos] >= 'a' && input[pos] <= 'z' || input[pos] >= '0' && input[pos] <= '9' || strings.ContainsRune("_-.:/", rune(input[pos]))) {
					pos++
				}
				value := input[begin:pos]
				if value != "player" && value != "minecraft:player" {
					return 0, errors.New("unsupported selector entity type (only player is supported)")
				}
				if !inverted {
					entities = false
				}
			case "limit", "x", "y", "z", "dx", "dy", "dz", "distance", "level":
				begin := pos
				for pos < len(input) && (input[pos] >= '0' && input[pos] <= '9' || input[pos] == '-' || input[pos] == '.') {
					pos++
				}
				value := input[begin:pos]
				switch key {
				case "limit":
					count, e := strconv.ParseInt(value, 10, 32)
					if e != nil || count < 1 || kind == 's' {
						return 0, errors.New("invalid or inapplicable selector limit")
					}
					single = count == 1
				case "distance", "level":
					if !commandNonnegativeRange(value, key == "level") {
						return 0, fmt.Errorf("invalid selector %s range", key)
					}
					if key == "level" {
						entities = false
					}
				default:
					number, e := strconv.ParseFloat(value, 64)
					if e != nil || math.IsInf(number, 0) || math.IsNaN(number) {
						return 0, errors.New("invalid selector coordinate")
					}
				}
			default:
				return 0, fmt.Errorf("unsupported selector option %q", key)
			}
			options[key] = 1
			if inverted {
				options[key] = 2
			}
			pos = commandSkipSpaces(input, pos)
			if pos == len(input) || input[pos] != ',' && input[pos] != ']' {
				return 0, errors.New("expected selector comma or closing bracket")
			}
			if input[pos] == ']' {
				break
			}
			pos++
		}
		if pos == len(input) {
			return 0, errors.New("unterminated entity selector")
		}
		pos++
	}
	if flags&packets.CommandEntitySingle != 0 && !single {
		return 0, errors.New("selector permits multiple entities in a single-entity argument")
	}
	if flags&packets.CommandEntityPlayersOnly != 0 && entities && kind != 's' {
		return 0, errors.New("selector includes entities in a players-only argument")
	}
	return pos, nil
}

func commandNonnegativeRange(value string, integer bool) bool {
	parts := strings.Split(value, "..")
	if len(parts) > 2 || value == "" || value == ".." {
		return false
	}
	bounds := [2]float64{0, math.Inf(1)}
	for i, part := range parts {
		if part == "" {
			continue
		}
		var number float64
		var err error
		if integer {
			var n int64
			n, err = strconv.ParseInt(part, 10, 32)
			number = float64(n)
		} else {
			number, err = strconv.ParseFloat(part, 64)
		}
		if err != nil || math.IsInf(number, 0) || math.IsNaN(number) || number < 0 {
			return false
		}
		bounds[i] = number
	}
	return bounds[0] <= bounds[1]
}
