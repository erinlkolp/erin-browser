package tui

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"erin-browser/internal/htmlrender"
)

// KeyType represents parsed keyboard input from a VT100/ANSI terminal.
type KeyType int

const (
	KeyNone KeyType = iota
	KeyRune
	KeyEnter
	KeyEsc
	KeyBackspace
	KeyDelete
	KeyTab
	KeyShiftTab
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyPgUp
	KeyPgDn
	KeyHome
	KeyEnd
	KeyCtrlC
	KeyCtrlL
	KeyCtrlU
	KeyCtrlW
)

// KeyEvent represents a single key press.
type KeyEvent struct {
	Type KeyType
	Rune rune
}

// KeyReader reads raw terminal bytes and decodes UTF-8 and ANSI escape sequences.
type KeyReader struct {
	fd  *os.File
	buf []byte
}

func NewKeyReader(f *os.File) *KeyReader {
	return &KeyReader{fd: f}
}

func (kr *KeyReader) readByte() (byte, error) {
	if len(kr.buf) > 0 {
		b := kr.buf[0]
		kr.buf = kr.buf[1:]
		return b, nil
	}
	tmp := make([]byte, 32)
	n, err := kr.fd.Read(tmp)
	if err != nil {
		return 0, err
	}
	if n > 1 {
		kr.buf = append(kr.buf, tmp[1:n]...)
	}
	return tmp[0], nil
}

// ReadKey blocks until the next key event arrives.
func (kr *KeyReader) ReadKey() (KeyEvent, error) {
	b, err := kr.readByte()
	if err != nil {
		return KeyEvent{}, err
	}

	switch b {
	case 3: // Ctrl-C
		return KeyEvent{Type: KeyCtrlC}, nil
	case 12: // Ctrl-L
		return KeyEvent{Type: KeyCtrlL}, nil
	case 21: // Ctrl-U
		return KeyEvent{Type: KeyCtrlU}, nil
	case 23: // Ctrl-W
		return KeyEvent{Type: KeyCtrlW}, nil
	case '\r', '\n':
		return KeyEvent{Type: KeyEnter}, nil
	case '\t':
		return KeyEvent{Type: KeyTab}, nil
	case 127, 8: // DEL or BS
		return KeyEvent{Type: KeyBackspace}, nil
	case 27: // ESC or CSI sequence
		return kr.readEscape()
	}

	// Decode multi-byte UTF-8 rune.
	if b < 0x80 {
		return KeyEvent{Type: KeyRune, Rune: rune(b)}, nil
	}
	seq := []byte{b}
	for !utf8.FullRune(seq) && len(seq) < 4 {
		nb, err := kr.readByte()
		if err != nil {
			break
		}
		seq = append(seq, nb)
	}
	r, _ := utf8.DecodeRune(seq)
	return KeyEvent{Type: KeyRune, Rune: r}, nil
}

func (kr *KeyReader) readEscape() (KeyEvent, error) {
	// If no buffered bytes follow immediately, read another byte only if available
	// via the already-read chunk; otherwise treat bare ESC as KeyEsc.
	if len(kr.buf) == 0 {
		return KeyEvent{Type: KeyEsc}, nil
	}
	b1, err := kr.readByte()
	if err != nil {
		return KeyEvent{Type: KeyEsc}, nil
	}
	if b1 == 'O' && len(kr.buf) > 0 {
		b2, _ := kr.readByte()
		switch b2 {
		case 'A':
			return KeyEvent{Type: KeyUp}, nil
		case 'B':
			return KeyEvent{Type: KeyDown}, nil
		case 'C':
			return KeyEvent{Type: KeyRight}, nil
		case 'D':
			return KeyEvent{Type: KeyLeft}, nil
		case 'H':
			return KeyEvent{Type: KeyHome}, nil
		case 'F':
			return KeyEvent{Type: KeyEnd}, nil
		}
		return KeyEvent{Type: KeyEsc}, nil
	}
	if b1 != '[' {
		return KeyEvent{Type: KeyEsc}, nil
	}

	var params []byte
	for len(kr.buf) > 0 {
		ch, _ := kr.readByte()
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || ch == '~' {
			return decodeCSI(params, ch), nil
		}
		params = append(params, ch)
	}
	return KeyEvent{Type: KeyEsc}, nil
}

func decodeCSI(params []byte, final byte) KeyEvent {
	p := string(params)
	switch final {
	case 'A':
		return KeyEvent{Type: KeyUp}
	case 'B':
		return KeyEvent{Type: KeyDown}
	case 'C':
		return KeyEvent{Type: KeyRight}
	case 'D':
		return KeyEvent{Type: KeyLeft}
	case 'H':
		return KeyEvent{Type: KeyHome}
	case 'F':
		return KeyEvent{Type: KeyEnd}
	case 'Z':
		return KeyEvent{Type: KeyShiftTab}
	case '~':
		switch p {
		case "1", "7":
			return KeyEvent{Type: KeyHome}
		case "3":
			return KeyEvent{Type: KeyDelete}
		case "4", "8":
			return KeyEvent{Type: KeyEnd}
		case "5":
			return KeyEvent{Type: KeyPgUp}
		case "6":
			return KeyEvent{Type: KeyPgDn}
		}
	}
	return KeyEvent{Type: KeyNone}
}

// ANSI control helpers.
const (
	ansiEnterAlt  = "\x1b[?1049h"
	ansiLeaveAlt  = "\x1b[?1049l"
	ansiHideCur   = "\x1b[?25l"
	ansiShowCur   = "\x1b[?25h"
	ansiClearAll  = "\x1b[2J\x1b[H"
	ansiClearLine = "\x1b[K"
	ansiReset     = "\x1b[0m"
)

func moveTo(b *bytes.Buffer, row, col int) {
	fmt.Fprintf(b, "\x1b[%d;%dH", row, col)
}

// writeBar writes a full-width top or bottom status bar in monochrome or color mode.
func writeBar(b *bytes.Buffer, row, width int, left, right string, mono bool, isHeader bool) {
	moveTo(b, row, 1)
	if mono {
		// Strict monochrome: reverse video only, zero color escape codes.
		b.WriteString("\x1b[0;7m")
	} else if isHeader {
		b.WriteString("\x1b[0;1;37;44m")
	} else {
		b.WriteString("\x1b[0;30;47m")
	}

	leftRunes := []rune(left)
	rightRunes := []rune(right)

	var line strings.Builder
	if len(rightRunes) > 0 && len(rightRunes)+2 < width {
		maxLeft := width - len(rightRunes) - 1
		if len(leftRunes) > maxLeft {
			if maxLeft > 3 {
				leftRunes = append(leftRunes[:maxLeft-3], '.', '.', '.')
			} else {
				leftRunes = leftRunes[:maxLeft]
			}
		}
		line.WriteString(string(leftRunes))
		pad := width - len(leftRunes) - len(rightRunes)
		if pad > 0 {
			line.WriteString(strings.Repeat(" ", pad))
		}
		line.WriteString(string(rightRunes))
	} else {
		if len(leftRunes) > width {
			if width > 3 {
				leftRunes = append(leftRunes[:width-3], '.', '.', '.')
			} else {
				leftRunes = leftRunes[:width]
			}
		}
		line.WriteString(string(leftRunes))
		if pad := width - len(leftRunes); pad > 0 {
			line.WriteString(strings.Repeat(" ", pad))
		}
	}

	b.WriteString(line.String())
	b.WriteString(ansiReset)
}

// styleSequence maps semantic SpanStyle + focus state to ANSI SGR sequences.
// In mono mode, NEVER emits color codes — only bold, underline, dim, and reverse video.
func styleSequence(st htmlrender.SpanStyle, focused bool, mono bool) string {
	if mono {
		if focused {
			return "\x1b[0;1;7m"
		}
		var codes []string
		codes = append(codes, "0")
		if st&(htmlrender.StyleBold|htmlrender.StyleHeading|htmlrender.StyleButton) != 0 {
			codes = append(codes, "1")
		}
		if st&(htmlrender.StyleUnderline|htmlrender.StyleLink|htmlrender.StyleInput) != 0 {
			codes = append(codes, "4")
		}
		if st&(htmlrender.StyleDim|htmlrender.StyleBadge|htmlrender.StyleQuote) != 0 && st&(htmlrender.StyleBold|htmlrender.StyleHeading) == 0 {
			codes = append(codes, "2")
		}
		return "\x1b[" + strings.Join(codes, ";") + "m"
	}

	// Clean 16-color ANSI palette (works on Linux VT, Sugar Terminal, xterm, SSH).
	if focused {
		return "\x1b[0;1;30;43m" // Bold black on yellow
	}
	switch {
	case st&htmlrender.StyleHeading != 0:
		return "\x1b[0;1;33m" // Bold yellow
	case st&htmlrender.StyleButton != 0:
		return "\x1b[0;1;32m" // Bold green
	case st&htmlrender.StyleInput != 0:
		return "\x1b[0;4;32m" // Underlined green
	case st&htmlrender.StyleLink != 0:
		return "\x1b[0;4;36m" // Underlined cyan
	case st&htmlrender.StyleBadge != 0:
		return "\x1b[0;35m" // Magenta link number
	case st&htmlrender.StyleCode != 0:
		return "\x1b[0;32m" // Green code
	case st&htmlrender.StyleQuote != 0:
		return "\x1b[0;36m" // Cyan quote
	case st&htmlrender.StyleBold != 0:
		return "\x1b[0;1m"
	case st&htmlrender.StyleUnderline != 0:
		return "\x1b[0;4m"
	case st&htmlrender.StyleDim != 0:
		return "\x1b[0;2m"
	default:
		return ansiReset
	}
}

// writeDocumentLine renders a single document line clipped to width, highlighting
// the focused target and optional case-insensitive search term.
func writeDocumentLine(b *bytes.Buffer, row, width int, line htmlrender.Line, focusedTargetID int, searchQuery string, mono bool) {
	moveTo(b, row, 1)
	col := 0
	lowerQuery := strings.ToLower(searchQuery)

	for _, sp := range line.Spans {
		if col >= width {
			break
		}
		runes := []rune(sp.Text)
		if col+len(runes) > width {
			runes = runes[:width-col]
		}
		text := string(runes)
		focused := focusedTargetID > 0 && sp.TargetID == focusedTargetID

		if lowerQuery == "" {
			b.WriteString(styleSequence(sp.Style, focused, mono))
			b.WriteString(text)
			col += len(runes)
			continue
		}

		// Highlight search matches within the span.
		lowerText := strings.ToLower(text)
		pos := 0
		for pos < len(text) {
			idx := strings.Index(lowerText[pos:], lowerQuery)
			if idx < 0 {
				b.WriteString(styleSequence(sp.Style, focused, mono))
				b.WriteString(text[pos:])
				break
			}
			if idx > 0 {
				b.WriteString(styleSequence(sp.Style, focused, mono))
				b.WriteString(text[pos : pos+idx])
			}
			end := pos + idx + len(lowerQuery)
			if end > len(text) {
				end = len(text)
			}
			if mono {
				b.WriteString("\x1b[0;7m")
			} else {
				b.WriteString("\x1b[0;1;30;47m")
			}
			b.WriteString(text[pos+idx : end])
			pos = end
		}
		col += len(runes)
	}

	b.WriteString(ansiReset)
	b.WriteString(ansiClearLine)
}
