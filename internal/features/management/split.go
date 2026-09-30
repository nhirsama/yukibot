package management

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// split tokenizes raw command arguments the way Python shlex.split does
// with posix=True and comments=False.
func split(input string) ([]string, error) {
	lex := lexer{rest: []rune(input), state: ' '}
	var tokens []string
	for {
		token, ok, err := lex.readToken()
		if err != nil {
			return nil, err
		}
		if !ok {
			return tokens, nil
		}
		tokens = append(tokens, token)
	}
}

type lexer struct {
	rest  []rune
	state rune
	token []rune
}

func (l *lexer) read() (rune, bool) {
	if len(l.rest) == 0 {
		return 0, false
	}
	next := l.rest[0]
	l.rest = l.rest[1:]
	return next, true
}

func isWhitespace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\r' || r == '\n'
}

func (l *lexer) readToken() (string, bool, error) {
	if l.state == 0 {
		return "", false, nil
	}
	l.token = l.token[:0]
	escapedState := ' '
	for {
		next, ok := l.read()
		switch l.state {
		case ' ':
			if !ok {
				l.state = 0
				return "", false, nil
			}
			if isWhitespace(next) {
				continue
			}
			if next == '\\' {
				escapedState = 'a'
				l.state = '\\'
				continue
			}
			if next == '\'' || next == '"' {
				l.state = next
				continue
			}
			l.token = append(l.token, next)
			l.state = 'a'
		case '\'', '"':
			if !ok {
				return "", false, errors.New("No closing quotation")
			}
			if next == l.state {
				l.state = 'a'
				continue
			}
			if l.state == '"' && next == '\\' {
				escapedState = l.state
				l.state = '\\'
				continue
			}
			l.token = append(l.token, next)
		case '\\':
			if !ok {
				return "", false, errors.New("No escaped character")
			}
			if (escapedState == '\'' || escapedState == '"') && next != '\\' && next != escapedState {
				l.token = append(l.token, '\\')
			}
			l.token = append(l.token, next)
			l.state = escapedState
		case 'a':
			if !ok {
				l.state = 0
				return string(l.token), true, nil
			}
			if isWhitespace(next) {
				l.state = ' '
				return string(l.token), true, nil
			}
			if next == '\\' {
				escapedState = 'a'
				l.state = '\\'
				continue
			}
			if next == '\'' || next == '"' {
				l.state = next
				continue
			}
			l.token = append(l.token, next)
		default:
			return "", false, nil
		}
	}
}

func parseUserID(token string) (int64, error) {
	invalid := &ValueError{Message: fmt.Sprintf("invalid literal for int() with base 10: '%s'", token)}
	number := strings.TrimSpace(token)
	if number == "" {
		return 0, invalid
	}
	negative := false
	switch number[0] {
	case '+':
		number = number[1:]
	case '-':
		negative = true
		number = number[1:]
	}
	if number == "" || number[0] == '_' {
		return 0, invalid
	}
	digits := make([]byte, 0, len(number))
	previousUnderscore := false
	for i := 0; i < len(number); i++ {
		character := number[i]
		if character == '_' {
			if previousUnderscore || len(digits) == 0 {
				return 0, invalid
			}
			previousUnderscore = true
			continue
		}
		if character < '0' || character > '9' {
			return 0, invalid
		}
		previousUnderscore = false
		digits = append(digits, character)
	}
	if previousUnderscore || len(digits) == 0 {
		return 0, invalid
	}
	literal := string(digits)
	if negative {
		literal = "-" + literal
	}
	value, err := strconv.ParseInt(literal, 10, 64)
	if err != nil {
		return 0, invalid
	}
	return value, nil
}
