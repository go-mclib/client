package chat

import (
	"bytes"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-mclib/data/registries"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

type commandNode struct {
	flags       byte
	children    []int
	redirect    int
	name        string
	parser      int
	stringMode  int
	entityFlags byte
}
type commandTree struct {
	nodes []commandNode
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
		return nil, fmt.Errorf("server has not advertised its command tree; try /help")
	}
	names := make([]string, 0, len(m.commandTree.nodes[m.commandTree.root].children))
	for _, id := range m.commandTree.nodes[m.commandTree.root].children {
		n := m.commandTree.nodes[id]
		if n.flags&3 == 1 {
			names = append(names, n.name)
		}
	}
	sort.Strings(names)
	return names, nil
}
func parseCommandTree(data []byte) (*commandTree, error) {
	if len(data) > 1<<20 {
		return nil, fmt.Errorf("command tree exceeds 1 MiB")
	}
	reader := bytes.NewReader(data)
	b := ns.NewReaderFrom(reader)
	count, err := b.ReadVarInt()
	if err != nil {
		return nil, err
	}
	if count < 1 || count > 32768 {
		return nil, fmt.Errorf("invalid command node count")
	}
	tree := &commandTree{nodes: make([]commandNode, int(count))}
	for i := range tree.nodes {
		n := &tree.nodes[i]
		n.redirect = -1
		n.parser = -1
		if n.flags, err = b.ReadByte(); err != nil {
			return nil, err
		}
		if n.flags&3 == 3 {
			return nil, fmt.Errorf("invalid command node type")
		}
		childCount, e := b.ReadVarInt()
		if e != nil {
			return nil, e
		}
		if childCount < 0 || childCount > count {
			return nil, fmt.Errorf("invalid command child count")
		}
		n.children = make([]int, int(childCount))
		for j := range n.children {
			v, e := b.ReadVarInt()
			if e != nil {
				return nil, e
			}
			if v < 0 || v >= count {
				return nil, fmt.Errorf("command child out of bounds")
			}
			n.children[j] = int(v)
		}
		if n.flags&8 != 0 {
			v, e := b.ReadVarInt()
			if e != nil {
				return nil, e
			}
			if v < 0 || v >= count {
				return nil, fmt.Errorf("command redirect out of bounds")
			}
			n.redirect = int(v)
		}
		if n.flags&3 != 0 {
			v, e := b.ReadString(32767)
			if e != nil {
				return nil, e
			}
			n.name = string(v)
		}
		if n.flags&3 == 2 {
			id, e := b.ReadVarInt()
			if e != nil {
				return nil, e
			}
			n.parser = int(id)
			// Resolve the generated registry before reading parser-specific properties.
			// Unknown parsers cannot be skipped: payload lengths are not on the wire.
			parser := registries.CommandArgumentType.ByID(int32(id))
			switch parser {
			case "brigadier:float", "brigadier:double", "brigadier:integer", "brigadier:long":
				flags, e := b.ReadByte()
				if e != nil {
					return nil, e
				}
				if flags&^byte(3) != 0 {
					return nil, fmt.Errorf("invalid numeric parser flags")
				}
				size := 4
				if parser == "brigadier:double" || parser == "brigadier:long" {
					size = 8
				}
				for mask := byte(1); mask <= 2; mask <<= 1 {
					if flags&mask != 0 {
						if _, e = b.ReadFixedByteArray(size); e != nil {
							return nil, e
						}
					}
				}
			case "brigadier:string":
				mode, e := b.ReadVarInt()
				if e != nil {
					return nil, e
				}
				if mode < 0 || mode > 2 {
					return nil, fmt.Errorf("invalid string parser mode")
				}
				n.stringMode = int(mode)
			case "minecraft:entity":
				if n.entityFlags, e = b.ReadByte(); e != nil {
					return nil, e
				}
				if n.entityFlags&^byte(3) != 0 {
					return nil, fmt.Errorf("invalid entity parser flags")
				}
			case "minecraft:score_holder":
				if _, e = b.ReadByte(); e != nil {
					return nil, e
				}
			case "minecraft:time":
				if _, e = b.ReadInt32(); e != nil {
					return nil, e
				}
			case "minecraft:resource_or_tag", "minecraft:resource_or_tag_key", "minecraft:resource", "minecraft:resource_key", "minecraft:resource_selector":
				if _, e = b.ReadString(32767); e != nil {
					return nil, e
				}
			default:
				if parser == "" {
					return nil, fmt.Errorf("unsupported command parser ID %d", n.parser)
				}
			}
		}
		if n.flags&16 != 0 {
			if n.flags&3 != 2 {
				return nil, fmt.Errorf("suggestions on non-argument node")
			}
			if _, err = b.ReadString(32767); err != nil {
				return nil, err
			}
		}
	}
	root, e := b.ReadVarInt()
	if e != nil {
		return nil, e
	}
	if root < 0 || root >= count || tree.nodes[root].flags&3 != 0 {
		return nil, fmt.Errorf("invalid command root")
	}
	tree.root = int(root)
	if reader.Len() != 0 {
		return nil, fmt.Errorf("trailing command tree bytes")
	}
	return tree, nil
}
func validCommandInput(command string) error {
	if !utf8.ValidString(command) || len(command) > 32767 || command == "" || strings.HasPrefix(command, "/") {
		return fmt.Errorf("invalid command length, encoding, or leading slash")
	}
	for _, r := range command {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return fmt.Errorf("commands must be one line without control characters")
		}
	}
	return nil
}
func (t *commandTree) hasSignable(id int, seen map[int]bool) bool {
	if seen[id] {
		return false
	}
	seen[id] = true
	n := t.nodes[id]
	if int32(n.parser) == registries.CommandArgumentType.Get("minecraft:message") {
		return true
	}
	if n.redirect >= 0 && n.redirect != t.root && t.hasSignable(n.redirect, seen) {
		return true
	}
	for _, child := range n.children {
		if t.hasSignable(child, seen) {
			return true
		}
	}
	return false
}
func (t *commandTree) arguments(command string, allowSelectors bool) ([]signableArgument, error) {
	rootWord := strings.SplitN(command, " ", 2)[0]
	for _, id := range t.nodes[t.root].children {
		n := t.nodes[id]
		if n.flags&3 != 1 || n.name != rootWord {
			continue
		}
		if !t.hasSignable(id, make(map[int]bool)) {
			return nil, nil
		}
		steps := 0
		args, err := t.walk(id, command, len(rootWord), nil, &steps, allowSelectors)
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
		return nil, fmt.Errorf("command graph traversal limit")
	}
	n := t.nodes[id]
	if pos == len(input) {
		if n.flags&4 != 0 {
			return args, nil
		}
		return nil, fmt.Errorf("incomplete command")
	}
	if input[pos] != ' ' {
		return nil, fmt.Errorf("expected argument separator")
	}
	pos++
	if n.redirect == t.root {
		// ArgumentVisitor(rejectRootRedirects=true) never visits this child
		// context, including any further aliases or nested root redirects.
		return args, nil
	}
	if n.redirect >= 0 {
		n = t.nodes[n.redirect]
	}
	wordEnd := pos
	for wordEnd < len(input) && input[wordEnd] != ' ' {
		wordEnd++
	}
	for _, child := range n.children {
		c := t.nodes[child]
		if c.flags&3 == 1 && c.name == input[pos:wordEnd] {
			return t.walk(child, input, wordEnd, args, steps, allowSelectors)
		}
	}
	argument := -1
	for _, child := range n.children {
		if t.nodes[child].flags&3 == 2 {
			if argument >= 0 {
				return nil, fmt.Errorf("unsupported competing argument branches")
			}
			argument = child
		}
	}
	if argument < 0 || pos == len(input) {
		return nil, fmt.Errorf("invalid or incomplete command")
	}
	c := t.nodes[argument]
	end, err := commandArgumentEnd(c, input, pos, allowSelectors)
	if err != nil {
		return nil, fmt.Errorf("argument %s: %w", c.name, err)
	}
	if int32(c.parser) == registries.CommandArgumentType.Get("minecraft:message") {
		if commandUTF16Length(c.name) > 16 || len(args) >= 8 {
			return nil, fmt.Errorf("signature name or count exceeds protocol limit")
		}
		args = append(args, signableArgument{c.name, input[pos:end]})
	}
	return t.walk(argument, input, end, args, steps, allowSelectors)
}

func commandUTF16Length(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r > 0xffff {
			n++
		}
	}
	return n
}

// Only parsers whose acceptance and span we can establish are used before a
// signed descendant. Other commands still use the ordinary unsigned path.
func commandArgumentEnd(n commandNode, input string, start int, allowSelectors bool) (int, error) {
	switch registries.CommandArgumentType.ByID(int32(n.parser)) {
	case "minecraft:message":
		if commandUTF16Length(input[start:]) > 256 {
			return 0, fmt.Errorf("message exceeds 256 UTF-16 units")
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
			return 0, fmt.Errorf("entity selector permission is not established; use a player name")
		}
		return commandEntityEnd(input, start, n.entityFlags)
	case "brigadier:string":
		if n.stringMode == 2 {
			return len(input), nil
		}
		end, _, err := commandString(input, start, n.stringMode == 1)
		if end == start && err == nil {
			err = fmt.Errorf("expected string")
		}
		return end, err
	case "brigadier:bool":
		end, token, err := commandString(input, start, false)
		if err == nil && token != "true" && token != "false" {
			err = fmt.Errorf("expected boolean")
		}
		return end, err
	default:
		return 0, fmt.Errorf("unsupported parser ID %d before signed message", n.parser)
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
					return 0, "", fmt.Errorf("invalid quoted string escape")
				}
				c = input[pos]
				pos++
			}
			value.WriteByte(c)
		}
		return 0, "", fmt.Errorf("unterminated quoted string")
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
		return 0, fmt.Errorf("expected entity")
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
			return 0, fmt.Errorf("unsupported noncanonical UUID target")
		}
		if !uuid && (name == "" || commandUTF16Length(name) > 16) {
			return 0, fmt.Errorf("invalid player name or UUID")
		}
		if uuid && flags&2 != 0 {
			return 0, fmt.Errorf("UUID includes entities in a players-only argument")
		}
		return end, nil
	}
	if start+1 == len(input) || !strings.ContainsRune("aenprs", rune(input[start+1])) {
		return 0, fmt.Errorf("invalid entity selector type")
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
				return 0, fmt.Errorf("expected selector option value")
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
						return 0, fmt.Errorf("invalid selector gamemode")
					}
					entities = false
				}
				if key == "sort" && (kind == 's' || value != "nearest" && value != "furthest" && value != "random" && value != "arbitrary") {
					return 0, fmt.Errorf("invalid or inapplicable selector sort")
				}
			case "type":
				begin := pos
				for pos < len(input) && (input[pos] >= 'a' && input[pos] <= 'z' || input[pos] >= '0' && input[pos] <= '9' || strings.ContainsRune("_-.:/", rune(input[pos]))) {
					pos++
				}
				value := input[begin:pos]
				if value != "player" && value != "minecraft:player" {
					return 0, fmt.Errorf("unsupported selector entity type (only player is supported)")
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
						return 0, fmt.Errorf("invalid or inapplicable selector limit")
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
						return 0, fmt.Errorf("invalid selector coordinate")
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
				return 0, fmt.Errorf("expected selector comma or closing bracket")
			}
			if input[pos] == ']' {
				break
			}
			pos++
		}
		if pos == len(input) {
			return 0, fmt.Errorf("unterminated entity selector")
		}
		pos++
	}
	if flags&1 != 0 && !single {
		return 0, fmt.Errorf("selector permits multiple entities in a single-entity argument")
	}
	if flags&2 != 0 && entities && kind != 's' {
		return 0, fmt.Errorf("selector includes entities in a players-only argument")
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
