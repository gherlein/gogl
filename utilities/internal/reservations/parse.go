package reservations

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/gherlein/gogl/src/types"
)

// Declaration is one parsed host block, with the line its block opened on so that
// duplicate reports point somewhere useful.
type Declaration struct {
	Reservation types.Reservation
	Line        int
}

// Directive keywords this format cares about. A host block may legally contain
// others, which are ignored.
const (
	directiveHardware = "hardware"
	directiveEthernet = "ethernet"
	directiveAddress  = "fixed-address"
	keywordHost       = "host"
)

// token is one lexical unit with the line it appeared on, so an error can name
// the offending line rather than the block.
type token struct {
	text string
	line int
}

// ParseHosts reads ISC DHCP host declarations, returning every declaration it
// could parse and every error it found.
//
// Errors are collected rather than returned on the first failure, so one run
// surfaces every problem in a file. Directives other than host blocks are
// ignored, so a real dhcpd.conf can be fed in unmodified.
func ParseHosts(r io.Reader) ([]Declaration, []error) {
	tokens, err := tokenize(r)
	if err != nil {
		return nil, []error{err}
	}

	var (
		declarations []Declaration
		errs         []error
	)

	for i := 0; i < len(tokens); {
		if tokens[i].text != keywordHost {
			i = skipDirective(tokens, i)
			continue
		}

		declaration, next, err := parseHostBlock(tokens, i)
		if err != nil {
			errs = append(errs, err)
		} else {
			declarations = append(declarations, declaration)
		}
		i = next
	}

	return declarations, errs
}

// tokenize splits the input into words, braces and semicolons, tracking lines.
// Working from a token stream rather than line by line is what makes the parser
// indifferent to how the input is wrapped.
func tokenize(r io.Reader) ([]token, error) {
	// Punctuation is separated so that "host a {" and "host a{" both tokenize the
	// same way.
	punctuation := strings.NewReplacer("{", " { ", "}", " } ", ";", " ; ")

	var tokens []token
	scanner := bufio.NewScanner(r)
	line := 0
	for scanner.Scan() {
		line++
		text := stripComment(scanner.Text())
		for _, word := range strings.Fields(punctuation.Replace(text)) {
			tokens = append(tokens, token{text: word, line: line})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read input: %w", err)
	}
	return tokens, nil
}

// stripComment removes a trailing comment. A '#' inside a value is not meaningful
// in this format, so this is safe.
func stripComment(line string) string {
	if idx := strings.Index(line, "#"); idx >= 0 {
		return line[:idx]
	}
	return line
}

// skipDirective advances past a non-host directive, whether it is a simple
// statement or a braced block such as subnet.
func skipDirective(tokens []token, i int) int {
	for ; i < len(tokens); i++ {
		switch tokens[i].text {
		case ";":
			return i + 1
		case "{":
			return skipBalanced(tokens, i)
		}
	}
	return i
}

// skipBalanced advances past a balanced brace group beginning at tokens[i] == "{".
func skipBalanced(tokens []token, i int) int {
	depth := 0
	for ; i < len(tokens); i++ {
		switch tokens[i].text {
		case "{":
			depth++
		case "}":
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return i
}

// hostBlock is the raw material of one declaration, before validation.
type hostBlock struct {
	name      string
	nameLine  int
	mac       string
	macLine   int
	address   string
	addrLine  int
	blockLine int
}

// parseHostBlock consumes one host block and returns the declaration it yields,
// the index just past the block, and any error.
func parseHostBlock(tokens []token, i int) (Declaration, int, error) {
	block := hostBlock{blockLine: tokens[i].line}
	i++ // past "host"

	if i >= len(tokens) {
		return Declaration{}, i, fmt.Errorf("line %d: host block has no hostname", block.blockLine)
	}
	if tokens[i].text == "{" {
		return Declaration{}, skipBalanced(tokens, i),
			fmt.Errorf("line %d: host block has no hostname", block.blockLine)
	}
	block.name, block.nameLine = tokens[i].text, tokens[i].line
	i++

	if i >= len(tokens) || tokens[i].text != "{" {
		return Declaration{}, i, fmt.Errorf("line %d: host %q is missing its opening brace",
			block.blockLine, block.name)
	}
	i++ // past "{"

	// Collect semicolon-terminated statements until the closing brace.
	statement := make([]token, 0, 4)
	for {
		if i >= len(tokens) {
			return Declaration{}, i, fmt.Errorf("line %d: unclosed host block for %q",
				block.blockLine, block.name)
		}

		switch tokens[i].text {
		case "}":
			if len(statement) > 0 {
				return Declaration{}, i + 1, fmt.Errorf(
					"line %d: statement %q is missing its terminating semicolon",
					statement[0].line, joinTokens(statement))
			}
			declaration, err := finishBlock(block)
			return declaration, i + 1, err

		case ";":
			if err := applyStatement(statement, &block); err != nil {
				return Declaration{}, skipToBlockEnd(tokens, i), err
			}
			statement = statement[:0]
			i++

		default:
			statement = append(statement, tokens[i])
			i++
		}
	}
}

// skipToBlockEnd advances past the closing brace of the block being parsed, so
// that one bad statement does not desynchronize the whole file.
func skipToBlockEnd(tokens []token, i int) int {
	for ; i < len(tokens); i++ {
		if tokens[i].text == "}" {
			return i + 1
		}
	}
	return i
}

func joinTokens(statement []token) string {
	words := make([]string, len(statement))
	for i, t := range statement {
		words[i] = t.text
	}
	return strings.Join(words, " ")
}

// applyStatement records the two statements this format cares about, ignoring any
// others a host block might legally contain.
func applyStatement(statement []token, block *hostBlock) error {
	if len(statement) == 0 {
		return nil
	}

	// A directive keyword appearing after the first position means the previous
	// statement was never terminated. Reporting that beats reporting the
	// downstream symptom of a missing MAC or address.
	for i := 1; i < len(statement); i++ {
		if statement[i].text == directiveHardware || statement[i].text == directiveAddress {
			return fmt.Errorf("line %d: statement %q is missing its terminating semicolon",
				statement[0].line, joinTokens(statement[:i]))
		}
	}

	words := make([]string, len(statement))
	for i, t := range statement {
		words[i] = t.text
	}

	switch {
	case len(words) == 3 && words[0] == directiveHardware && words[1] == directiveEthernet:
		block.mac, block.macLine = words[2], statement[2].line
	case len(words) == 2 && words[0] == directiveAddress:
		block.address, block.addrLine = words[1], statement[1].line
	}
	return nil
}

// finishBlock validates a complete block into a Declaration.
func finishBlock(block hostBlock) (Declaration, error) {
	if block.mac == "" {
		return Declaration{}, fmt.Errorf("line %d: host %q has no %s %s statement",
			block.blockLine, block.name, directiveHardware, directiveEthernet)
	}
	if block.address == "" {
		return Declaration{}, fmt.Errorf("line %d: host %q has no %s statement",
			block.blockLine, block.name, directiveAddress)
	}

	if err := types.ValidateName(block.name); err != nil {
		return Declaration{}, fmt.Errorf("line %d: %w", block.nameLine, err)
	}

	mac, err := types.NormalizeMAC(block.mac)
	if err != nil {
		return Declaration{}, fmt.Errorf("line %d: host %q: %w", block.macLine, block.name, err)
	}

	ip := net.ParseIP(block.address)
	if ip == nil || ip.To4() == nil {
		return Declaration{}, fmt.Errorf("line %d: host %q: %q is not a valid IPv4 address",
			block.addrLine, block.name, block.address)
	}

	return Declaration{
		Reservation: types.Reservation{
			Name: block.name,
			MAC:  mac,
			IP:   ip.String(),
		},
		Line: block.blockLine,
	}, nil
}
