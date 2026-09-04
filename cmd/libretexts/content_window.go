package main

import "fmt"

type contentWindow struct {
	Offset        int  `json:"offset"`
	ReturnedChars int  `json:"returned_chars"`
	TotalChars    int  `json:"total_chars"`
	Truncated     bool `json:"truncated"`
	NextOffset    *int `json:"next_offset,omitempty"`
}

func selectContent(value string, offset, maxChars int) (string, contentWindow, error) {
	if offset < 0 {
		return "", contentWindow{}, newAgentError("INVALID_ARGUMENT", fmt.Sprintf("offset must be non-negative, got %d", offset), "Provide an offset of zero or greater.", false, nil)
	}
	if maxChars < 0 {
		return "", contentWindow{}, newAgentError("INVALID_ARGUMENT", fmt.Sprintf("max-chars must be non-negative, got %d", maxChars), "Provide a max-chars value of zero or greater.", false, nil)
	}

	runes := []rune(value)
	if offset > len(runes) {
		return "", contentWindow{}, newAgentError("INVALID_ARGUMENT", fmt.Sprintf("offset %d is past the content length %d", offset, len(runes)), "Provide an offset at or before the end of the content.", false, nil)
	}

	end := len(runes)
	if maxChars > 0 && maxChars < len(runes)-offset {
		end = offset + maxChars
	}
	selected := string(runes[offset:end])
	window := contentWindow{
		Offset:        offset,
		ReturnedChars: end - offset,
		TotalChars:    len(runes),
		Truncated:     end < len(runes),
	}
	if window.Truncated {
		window.NextOffset = intPointer(end)
	}
	return selected, window, nil
}

func intPointer(value int) *int {
	return &value
}
