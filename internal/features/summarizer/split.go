package summarizer

import "errors"

// splitShell tokenizes raw command arguments the way Python shlex.split does
// with posix=True and comments=False.
func splitShell(input string) ([]string, error) {
	lex := shellLexer{rest: []rune(input), state: ' '}
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

type shellLexer struct {
	rest  []rune
	state rune
	token []rune
}

func (l *shellLexer) read() (rune, bool) {
	if len(l.rest) == 0 {
		return 0, false
	}
	next := l.rest[0]
	l.rest = l.rest[1:]
	return next, true
}

func isShellWhitespace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\r' || r == '\n'
}

func (l *shellLexer) readToken() (string, bool, error) {
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
			if isShellWhitespace(next) {
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
			if isShellWhitespace(next) {
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
