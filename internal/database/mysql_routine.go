package database

import "errors"

type mysqlRoutineBlock struct {
	kind         string
	expression   bool
	expectsValue bool
}

func mysqlRoutineStatementStart(word string) bool {
	switch word {
	case "BEGIN", "IF", "CASE", "LOOP", "WHILE", "REPEAT", "RETURN", "SET", "SELECT", "INSERT", "UPDATE", "DELETE", "REPLACE", "CALL", "SIGNAL", "RESIGNAL",
		"DO", "OPEN", "CLOSE", "FETCH", "GET", "PREPARE", "EXECUTE", "DEALLOCATE", "COMMIT", "ROLLBACK", "SAVEPOINT", "RELEASE", "START", "CREATE", "ALTER", "DROP", "TRUNCATE",
		"SHOW", "DESCRIBE", "DESC", "EXPLAIN", "ANALYZE", "OPTIMIZE", "CHECK", "REPAIR":
		return true
	}
	return false
}

// Delay END until the next token so comments between END and its suffix are
// harmless. Track statement positions too: BEGIN and END are legal identifiers.
type mysqlRoutineBlocks struct {
	kind           string
	stack          []mysqlRoutineBlock
	pendingEnd     bool
	endIdentifier  bool
	qualified      bool
	bodyReady      bool
	inBody         bool
	statementStart bool
	handler        bool
	previousWord   string
}

func (b *mysqlRoutineBlocks) finishEnd(suffix string) error {
	if !b.pendingEnd {
		return nil
	}
	b.pendingEnd = false
	if b.endIdentifier {
		switch suffix {
		case "IF", "LOOP", "WHILE", "REPEAT", "CASE":
		default:
			return nil // END used in an expression is an identifier.
		}
	}
	if len(b.stack) == 0 {
		return errors.New("unbalanced MySQL routine body")
	}
	top := b.stack[len(b.stack)-1]
	switch suffix {
	case "IF", "LOOP", "WHILE", "REPEAT", "CASE":
		if top.kind != suffix || top.expression {
			return errors.New("unbalanced MySQL routine body")
		}
	default:
		if top.kind != "BEGIN" && !top.expression {
			return errors.New("MySQL control block requires an END suffix")
		}
	}
	b.stack = b.stack[:len(b.stack)-1]
	if len(b.stack) > 0 && b.stack[len(b.stack)-1].expression {
		b.stack[len(b.stack)-1].expectsValue = false
	}
	return nil
}

func (b *mysqlRoutineBlocks) symbol(ch byte, depth int) error {
	if err := b.finishEnd(""); err != nil {
		return err
	}
	b.qualified = ch == '.'
	b.previousWord = ""
	if ch == ')' && depth == 1 && (b.kind == "PROCEDURE" || b.kind == "FUNCTION") {
		b.bodyReady = true
	}
	if b.inBody {
		b.statementStart = ch == ';' || ch == ':'
		if len(b.stack) > 0 && b.stack[len(b.stack)-1].expression {
			top := &b.stack[len(b.stack)-1]
			switch ch {
			case '(', ',', '+', '-', '*', '/', '%', '=', '<', '>', '!', '|', '&', '^', '~':
				top.expectsValue = true
			case ';', ':', '.':
			default:
				top.expectsValue = false
			}
		}
	}
	return nil
}

func (b *mysqlRoutineBlocks) word(word string, depth int) error {
	if b.qualified {
		b.qualified = false
		b.previousWord = ""
		b.statementStart = false
		if len(b.stack) > 0 && b.stack[len(b.stack)-1].expression {
			b.stack[len(b.stack)-1].expectsValue = false
		}
		return nil // Even reserved words are identifiers after a dot in MySQL.
	}
	wasEnd := b.pendingEnd
	if err := b.finishEnd(word); err != nil {
		return err
	}
	previous := b.previousWord
	b.previousWord = word
	if wasEnd {
		switch word {
		case "IF", "LOOP", "WHILE", "REPEAT", "CASE":
			b.statementStart = false
			return nil // Closing suffixes must not open another block.
		}
	}
	if !b.inBody {
		if depth != 0 {
			return nil
		}
		if b.kind == "TRIGGER" && previous == "EACH" && word == "ROW" || b.kind == "EVENT" && word == "DO" {
			b.bodyReady = true
			return nil
		}
		if !b.bodyReady {
			return nil
		}
		if b.kind == "FUNCTION" && word == "SET" && (previous == "RETURNS" || previous == "CHARACTER") {
			return nil // SET return types and CHARACTER SET belong to the header.
		}
		if !mysqlRoutineStatementStart(word) {
			return nil // Routine names, types and characteristics precede the body.
		}
		b.inBody, b.statementStart = true, true
	}
	if previous == "HANDLER" && word == "FOR" {
		b.handler = true
	}
	if b.handler && depth == 0 && mysqlRoutineStatementStart(word) {
		b.statementStart, b.handler = true, false
	}
	expectsValue := false
	if len(b.stack) > 0 && b.stack[len(b.stack)-1].expression {
		top := &b.stack[len(b.stack)-1]
		expectsValue = top.expectsValue
		switch word {
		case "WHEN", "THEN", "ELSE", "AND", "OR", "XOR", "NOT", "IS", "IN", "LIKE", "REGEXP", "BETWEEN":
			top.expectsValue = true
		default:
			top.expectsValue = false
		}
	}
	switch word {
	case "BEGIN":
		if depth == 0 && (b.statementStart || b.handler) {
			b.stack = append(b.stack, mysqlRoutineBlock{kind: word})
			b.statementStart, b.handler = true, false
			return nil
		}
	case "CASE":
		b.stack = append(b.stack, mysqlRoutineBlock{kind: word, expression: !b.statementStart, expectsValue: true})
	case "IF", "LOOP", "WHILE", "REPEAT":
		if depth == 0 && b.statementStart {
			b.stack = append(b.stack, mysqlRoutineBlock{kind: word})
			b.statementStart = word == "LOOP" || word == "REPEAT"
			return nil
		}
	case "END":
		b.pendingEnd = true
		b.endIdentifier = !b.statementStart && (expectsValue || !(len(b.stack) > 0 && b.stack[len(b.stack)-1].expression))
	case "THEN", "ELSE", "DO":
		if len(b.stack) > 0 && !b.stack[len(b.stack)-1].expression {
			b.statementStart = true
			return nil
		}
	case "SET", "SELECT", "INSERT", "UPDATE", "DELETE", "REPLACE", "CALL", "SIGNAL", "RESIGNAL":
		b.handler = false
	}
	b.statementStart = false
	return nil
}
