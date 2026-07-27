package model

import "time"

// LineClass is the render classification chosen per output line.
// The classifier never mutates the raw text.
type LineClass string

const (
	ClassNormal      LineClass = "normal"
	ClassStderr      LineClass = "stderr"
	ClassCommand     LineClass = "command"
	ClassDestructive LineClass = "destructive"
	ClassDevice      LineClass = "device"
	ClassWarning     LineClass = "warning"
	ClassError       LineClass = "error"
	ClassSuccess     LineClass = "success"
	ClassMarker      LineClass = "marker"
	ClassStage       LineClass = "stage"
)

// OutputLine is one rendered row in the output panel. Raw is the
// untouched source text; Class is the render hint.
type OutputLine struct {
	Time   time.Time
	Stream string
	Raw    string
	Class  LineClass
}

// Output is the ring of recent lines shown in the bottom-right panel.
// Capacity is enforced by the engine when it appends.
type Output struct {
	Lines []OutputLine
}
