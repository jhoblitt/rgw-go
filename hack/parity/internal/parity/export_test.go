package parity

import (
	"maps"
	"slices"
)

// SelectionIDs returns the ids s selects, sorted, for the specs.
func SelectionIDs(s Selection) []string {
	return slices.Sorted(maps.Keys(s.ids))
}

// DroppedSummary exposes droppedSummary to the specs.
var DroppedSummary = droppedSummary

// CodeSpan exposes codeSpan to the specs.
var CodeSpan = codeSpan

// MarkLine exposes markLine to the specs.
var MarkLine = markLine

// IniMarkers exposes iniMarkers to the specs.
var IniMarkers = iniMarkers

// ReadPinFile exposes readPinFile to the specs.
var ReadPinFile = readPinFile

// PinSourceProblems exposes pinSourceProblems to the specs.
var PinSourceProblems = pinSourceProblems

// FetchTreeless exposes fetchTreeless to the specs.
var FetchTreeless = fetchTreeless
